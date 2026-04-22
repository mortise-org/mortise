package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type MetricEntry struct {
	Ts        int64
	Pod       string
	Namespace string
	App       string
	Env       string
	CPU       float64
	Memory    int64
}

type LogEntry struct {
	Ts        string
	Pod       string
	Namespace string
	App       string
	Env       string
	Stream    string
	Line      string
}

type PodMetricsSeries struct {
	Name   string       `json:"name"`
	CPU    [][2]float64 `json:"cpu"`
	Memory [][2]float64 `json:"memory"`
}

type LogLine struct {
	Ts     string `json:"ts"`
	Pod    string `json:"pod"`
	Text   string `json:"text"`
	Stream string `json:"stream,omitempty"`
}

type Store struct {
	db *sql.DB
}

func NewStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}

	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS metrics (
			ts        INTEGER NOT NULL,
			pod       TEXT NOT NULL,
			namespace TEXT NOT NULL,
			app       TEXT NOT NULL,
			env       TEXT NOT NULL,
			cpu       REAL NOT NULL,
			memory    INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_metrics_query ON metrics(namespace, app, env, ts)`,
		`CREATE TABLE IF NOT EXISTS logs (
			ts        TEXT NOT NULL,
			pod       TEXT NOT NULL,
			namespace TEXT NOT NULL,
			app       TEXT NOT NULL,
			env       TEXT NOT NULL,
			stream    TEXT NOT NULL DEFAULT 'stdout',
			line      TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_query ON logs(namespace, app, env, ts)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			return nil, fmt.Errorf("exec DDL: %w", err)
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) InsertMetrics(entries []MetricEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare("INSERT INTO metrics (ts, pod, namespace, app, env, cpu, memory) VALUES (?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, e := range entries {
		if _, err := stmt.Exec(e.Ts, e.Pod, e.Namespace, e.App, e.Env, e.CPU, e.Memory); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) QueryMetrics(namespace, app, env string, start, end, step int64) ([]PodMetricsSeries, error) {
	rows, err := s.db.Query(
		`SELECT pod, (ts / ? * ?) AS bucket, AVG(cpu), AVG(memory)
		 FROM metrics
		 WHERE namespace = ? AND app = ? AND env = ? AND ts >= ? AND ts <= ?
		 GROUP BY pod, bucket
		 ORDER BY pod, bucket`,
		step, step, namespace, app, env, start, end,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byPod := map[string]*PodMetricsSeries{}
	var order []string
	for rows.Next() {
		var pod string
		var bucket int64
		var cpu, mem float64
		if err := rows.Scan(&pod, &bucket, &cpu, &mem); err != nil {
			return nil, err
		}
		ps, ok := byPod[pod]
		if !ok {
			ps = &PodMetricsSeries{Name: pod}
			byPod[pod] = ps
			order = append(order, pod)
		}
		ps.CPU = append(ps.CPU, [2]float64{float64(bucket), cpu})
		ps.Memory = append(ps.Memory, [2]float64{float64(bucket), mem})
	}

	result := make([]PodMetricsSeries, 0, len(order))
	for _, name := range order {
		result = append(result, *byPod[name])
	}
	return result, rows.Err()
}

func (s *Store) InsertLog(e LogEntry) error {
	_, err := s.db.Exec(
		"INSERT INTO logs (ts, pod, namespace, app, env, stream, line) VALUES (?, ?, ?, ?, ?, ?, ?)",
		e.Ts, e.Pod, e.Namespace, e.App, e.Env, e.Stream, e.Line,
	)
	return err
}

func (s *Store) QueryLogs(namespace, app, env string, start, end int64, limit int, filter, before string) ([]LogLine, bool, error) {
	var args []any
	q := strings.Builder{}
	q.WriteString("SELECT ts, pod, line, stream FROM logs WHERE namespace = ? AND app = ? AND env = ?")
	args = append(args, namespace, app, env)

	startTS := time.Unix(start, 0).UTC().Format(time.RFC3339Nano)
	endTS := time.Unix(end, 0).UTC().Format(time.RFC3339Nano)
	q.WriteString(" AND ts >= ? AND ts <= ?")
	args = append(args, startTS, endTS)

	if before != "" {
		q.WriteString(" AND ts < ?")
		args = append(args, before)
	}
	if filter != "" {
		q.WriteString(" AND line LIKE '%' || ? || '%'")
		args = append(args, filter)
	}

	q.WriteString(" ORDER BY ts DESC LIMIT ?")
	args = append(args, limit+1)

	rows, err := s.db.Query(q.String(), args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	var lines []LogLine
	for rows.Next() {
		var l LogLine
		if err := rows.Scan(&l.Ts, &l.Pod, &l.Text, &l.Stream); err != nil {
			return nil, false, err
		}
		lines = append(lines, l)
	}
	if lines == nil {
		lines = []LogLine{}
	}

	hasMore := len(lines) > limit
	if hasMore {
		lines = lines[:limit]
	}
	return lines, hasMore, rows.Err()
}

func (s *Store) Trim(metricsRetention, logRetention time.Duration) (int64, int64, error) {
	metricsCutoff := time.Now().Add(-metricsRetention).Unix()
	res, err := s.db.Exec("DELETE FROM metrics WHERE ts < ?", metricsCutoff)
	if err != nil {
		return 0, 0, err
	}
	metricsDeleted, _ := res.RowsAffected()

	logsCutoff := time.Now().Add(-logRetention).UTC().Format(time.RFC3339Nano)
	res, err = s.db.Exec("DELETE FROM logs WHERE ts < ?", logsCutoff)
	if err != nil {
		return metricsDeleted, 0, err
	}
	logsDeleted, _ := res.RowsAffected()

	return metricsDeleted, logsDeleted, nil
}
