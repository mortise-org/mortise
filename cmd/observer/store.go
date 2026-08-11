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

type TrafficEntry struct {
	Ts        int64
	Namespace string
	App       string
	Env       string
	// Project is carried for the Prometheus exposition only; the traffic
	// table keys on namespace like every other series.
	Project    string
	Requests   int64
	Status2xx  int64
	Status3xx  int64
	Status4xx  int64
	Status5xx  int64
	LatencyP50 float64
	LatencyP95 float64
	LatencyP99 float64
	BytesIn    int64
	BytesOut   int64
}

type TrafficSeries struct {
	Requests   [][2]float64 `json:"requests"`
	Status2xx  [][2]float64 `json:"status2xx"`
	Status3xx  [][2]float64 `json:"status3xx"`
	Status4xx  [][2]float64 `json:"status4xx"`
	Status5xx  [][2]float64 `json:"status5xx"`
	LatencyP50 [][2]float64 `json:"latencyP50"`
	LatencyP95 [][2]float64 `json:"latencyP95"`
	LatencyP99 [][2]float64 `json:"latencyP99"`
	BytesIn    [][2]float64 `json:"bytesIn"`
	BytesOut   [][2]float64 `json:"bytesOut"`
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
		`CREATE TABLE IF NOT EXISTS traffic (
			ts          INTEGER NOT NULL,
			namespace   TEXT NOT NULL,
			app         TEXT NOT NULL,
			env         TEXT NOT NULL,
			requests    INTEGER NOT NULL,
			status_2xx  INTEGER NOT NULL DEFAULT 0,
			status_3xx  INTEGER NOT NULL DEFAULT 0,
			status_4xx  INTEGER NOT NULL DEFAULT 0,
			status_5xx  INTEGER NOT NULL DEFAULT 0,
			latency_p50 REAL NOT NULL DEFAULT 0,
			latency_p95 REAL NOT NULL DEFAULT 0,
			latency_p99 REAL NOT NULL DEFAULT 0,
			bytes_in    INTEGER NOT NULL DEFAULT 0,
			bytes_out   INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_traffic_query ON traffic(namespace, app, env, ts)`,
		// Collector heartbeats are the gap-visibility primitive: a window
		// with rows here but no series rows was OBSERVED EMPTY; a window
		// with no rows here was NOT OBSERVED (collector down/erroring) and
		// must render as a gap, never be interpolated over.
		`CREATE TABLE IF NOT EXISTS collector_ticks (
			collector TEXT NOT NULL,
			ts        INTEGER NOT NULL,
			ok        INTEGER NOT NULL,
			items     INTEGER NOT NULL DEFAULT 0,
			error     TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ticks_query ON collector_ticks(collector, ts)`,
		`CREATE TABLE IF NOT EXISTS pvc_metrics (
			ts        INTEGER NOT NULL,
			namespace TEXT NOT NULL,
			app       TEXT NOT NULL,
			env       TEXT NOT NULL,
			pvc       TEXT NOT NULL,
			capacity  INTEGER NOT NULL,
			used      INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pvc_query ON pvc_metrics(namespace, app, env, ts)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			return nil, fmt.Errorf("exec DDL: %w", err)
		}
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

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

func (s *Store) InsertLogs(entries []LogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare("INSERT INTO logs (ts, pod, namespace, app, env, stream, line) VALUES (?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, e := range entries {
		if _, err := stmt.Exec(e.Ts, e.Pod, e.Namespace, e.App, e.Env, e.Stream, e.Line); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// LastLogTimestamp returns the newest stored log timestamp for a pod ("" when
// none). Tailers resume from it so a restart neither re-ingests the tail nor
// silently drops the downtime window (within kubelet log rotation limits).
func (s *Store) LastLogTimestamp(namespace, pod string) (string, error) {
	var ts sql.NullString
	err := s.db.QueryRow(
		"SELECT MAX(ts) FROM logs WHERE namespace = ? AND pod = ?",
		namespace, pod,
	).Scan(&ts)
	if err != nil {
		return "", err
	}
	return ts.String, nil
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

func (s *Store) InsertTraffic(entries []TrafficEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO traffic (ts, namespace, app, env, requests, status_2xx, status_3xx, status_4xx, status_5xx, latency_p50, latency_p95, latency_p99, bytes_in, bytes_out) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, e := range entries {
		if _, err := stmt.Exec(e.Ts, e.Namespace, e.App, e.Env, e.Requests, e.Status2xx, e.Status3xx, e.Status4xx, e.Status5xx, e.LatencyP50, e.LatencyP95, e.LatencyP99, e.BytesIn, e.BytesOut); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) QueryTraffic(namespace, app, env string, start, end, step int64) (*TrafficSeries, error) {
	rows, err := s.db.Query(
		`SELECT (ts / ? * ?) AS bucket,
			SUM(requests), SUM(status_2xx), SUM(status_3xx), SUM(status_4xx), SUM(status_5xx),
			CASE WHEN SUM(requests) > 0 THEN SUM(latency_p50 * requests) / SUM(requests) ELSE 0 END,
			CASE WHEN SUM(requests) > 0 THEN SUM(latency_p95 * requests) / SUM(requests) ELSE 0 END,
			CASE WHEN SUM(requests) > 0 THEN SUM(latency_p99 * requests) / SUM(requests) ELSE 0 END,
			SUM(bytes_in), SUM(bytes_out)
		 FROM traffic
		 WHERE namespace = ? AND app = ? AND env = ? AND ts >= ? AND ts <= ?
		 GROUP BY bucket
		 ORDER BY bucket`,
		step, step, namespace, app, env, start, end,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	series := &TrafficSeries{}
	for rows.Next() {
		var bucket int64
		var reqs, s2xx, s3xx, s4xx, s5xx float64
		var lp50, lp95, lp99 float64
		var bytesIn, bytesOut float64
		if err := rows.Scan(&bucket, &reqs, &s2xx, &s3xx, &s4xx, &s5xx, &lp50, &lp95, &lp99, &bytesIn, &bytesOut); err != nil {
			return nil, err
		}
		ts := float64(bucket)
		series.Requests = append(series.Requests, [2]float64{ts, reqs})
		series.Status2xx = append(series.Status2xx, [2]float64{ts, s2xx})
		series.Status3xx = append(series.Status3xx, [2]float64{ts, s3xx})
		series.Status4xx = append(series.Status4xx, [2]float64{ts, s4xx})
		series.Status5xx = append(series.Status5xx, [2]float64{ts, s5xx})
		series.LatencyP50 = append(series.LatencyP50, [2]float64{ts, lp50})
		series.LatencyP95 = append(series.LatencyP95, [2]float64{ts, lp95})
		series.LatencyP99 = append(series.LatencyP99, [2]float64{ts, lp99})
		series.BytesIn = append(series.BytesIn, [2]float64{ts, bytesIn})
		series.BytesOut = append(series.BytesOut, [2]float64{ts, bytesOut})
	}
	return series, rows.Err()
}

// Tick records one collector cycle. ok=false ticks carry the error summary;
// items is how many series rows the cycle produced (0 is a valid,
// observed-empty cycle).
type Tick struct {
	Collector string
	Ts        int64
	OK        bool
	Items     int
	Error     string
}

func (s *Store) InsertTick(t Tick) error {
	ok := 0
	if t.OK {
		ok = 1
	}
	_, err := s.db.Exec(
		"INSERT INTO collector_ticks (collector, ts, ok, items, error) VALUES (?, ?, ?, ?, ?)",
		t.Collector, t.Ts, ok, t.Items, t.Error,
	)
	return err
}

// QueryCoverage buckets successful heartbeats for a collector over
// [start,end]: [bucketTs, 1] where at least one successful tick landed in the
// bucket, [bucketTs, 0] where none did. Consumers render 0-buckets as gaps.
func (s *Store) QueryCoverage(collector string, start, end, step int64) ([][2]int64, error) {
	rows, err := s.db.Query(
		`SELECT (ts / ? * ?) AS bucket, MAX(ok)
		 FROM collector_ticks
		 WHERE collector = ? AND ts >= ? AND ts <= ?
		 GROUP BY bucket`,
		step, step, collector, start, end,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	observed := map[int64]int64{}
	for rows.Next() {
		var bucket, ok int64
		if err := rows.Scan(&bucket, &ok); err != nil {
			return nil, err
		}
		observed[bucket] = ok
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out [][2]int64
	for b := (start / step) * step; b <= end; b += step {
		out = append(out, [2]int64{b, observed[b]})
	}
	return out, nil
}

// LastTicks returns the most recent tick per collector (for self-health).
func (s *Store) LastTicks() ([]Tick, error) {
	rows, err := s.db.Query(
		`SELECT collector, ts, ok, items, error FROM collector_ticks t
		 WHERE ts = (SELECT MAX(ts) FROM collector_ticks WHERE collector = t.collector)
		 GROUP BY collector`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Tick
	for rows.Next() {
		var t Tick
		var ok int
		if err := rows.Scan(&t.Collector, &t.Ts, &ok, &t.Items, &t.Error); err != nil {
			return nil, err
		}
		t.OK = ok == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

type PVCEntry struct {
	Ts        int64
	Namespace string
	App       string
	Env       string
	PVC       string
	Capacity  int64
	Used      int64
}

type PVCSeries struct {
	Name     string       `json:"name"`
	Capacity [][2]float64 `json:"capacity"`
	Used     [][2]float64 `json:"used"`
}

func (s *Store) InsertPVCMetrics(entries []PVCEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare("INSERT INTO pvc_metrics (ts, namespace, app, env, pvc, capacity, used) VALUES (?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, e := range entries {
		if _, err := stmt.Exec(e.Ts, e.Namespace, e.App, e.Env, e.PVC, e.Capacity, e.Used); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) QueryPVCMetrics(namespace, app, env string, start, end, step int64) ([]PVCSeries, error) {
	rows, err := s.db.Query(
		`SELECT pvc, (ts / ? * ?) AS bucket, AVG(capacity), AVG(used)
		 FROM pvc_metrics
		 WHERE namespace = ? AND app = ? AND env = ? AND ts >= ? AND ts <= ?
		 GROUP BY pvc, bucket
		 ORDER BY pvc, bucket`,
		step, step, namespace, app, env, start, end,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byPVC := map[string]*PVCSeries{}
	var order []string
	for rows.Next() {
		var pvc string
		var bucket int64
		var capacity, used float64
		if err := rows.Scan(&pvc, &bucket, &capacity, &used); err != nil {
			return nil, err
		}
		ps, ok := byPVC[pvc]
		if !ok {
			ps = &PVCSeries{Name: pvc}
			byPVC[pvc] = ps
			order = append(order, pvc)
		}
		ps.Capacity = append(ps.Capacity, [2]float64{float64(bucket), capacity})
		ps.Used = append(ps.Used, [2]float64{float64(bucket), used})
	}

	result := make([]PVCSeries, 0, len(order))
	for _, name := range order {
		result = append(result, *byPVC[name])
	}
	return result, rows.Err()
}

const trimChunkSize = 1000

// downsampleAge is how long metrics stay at raw poll-interval resolution
// before being collapsed to downsampleStep buckets. Retention policy: raw
// points for 24h, 5-minute averages until the age cutoff, everything gone
// after it. Traffic is already bucket-aggregated at collection time and
// logs cannot be downsampled, so both are age-trimmed only.
const downsampleAge = 24 * time.Hour
const downsampleStep = int64(300)

func (s *Store) Trim(metricsRetention, logRetention, trafficRetention time.Duration) (int64, int64, error) {
	metricsCutoff := time.Now().Add(-metricsRetention).Unix()
	metricsDeleted, err := s.chunkedDelete("metrics", "ts < ?", metricsCutoff)
	if err != nil {
		return 0, 0, err
	}
	if err := s.downsampleMetrics(time.Now().Add(-downsampleAge).Unix()); err != nil {
		return metricsDeleted, 0, err
	}

	logsCutoff := time.Now().Add(-logRetention).UTC().Format(time.RFC3339Nano)
	logsDeleted, err := s.chunkedDelete("logs", "ts < ?", logsCutoff)
	if err != nil {
		return metricsDeleted, 0, err
	}

	trafficCutoff := time.Now().Add(-trafficRetention).Unix()
	if _, err := s.chunkedDelete("traffic", "ts < ?", trafficCutoff); err != nil {
		return metricsDeleted, logsDeleted, err
	}

	// Heartbeats and PVC samples follow the metrics window.
	if _, err := s.chunkedDelete("collector_ticks", "ts < ?", metricsCutoff); err != nil {
		return metricsDeleted, logsDeleted, err
	}
	if _, err := s.chunkedDelete("pvc_metrics", "ts < ?", metricsCutoff); err != nil {
		return metricsDeleted, logsDeleted, err
	}

	return metricsDeleted, logsDeleted, nil
}

// downsampleMetrics collapses metric rows older than cutoff into one averaged
// row per (pod, series, downsampleStep bucket). Idempotent: already-collapsed
// rows re-collapse to themselves, and the bucket timestamp marker (ts aligned
// to the bucket) makes the second pass a no-op via the row-count guard.
func (s *Store) downsampleMetrics(cutoff int64) error {
	// Skip when there is nothing finer than the target resolution: collapsed
	// rows sit exactly on bucket boundaries, so any off-boundary row below
	// the cutoff means work to do.
	var pending int
	if err := s.db.QueryRow(
		"SELECT COUNT(1) FROM metrics WHERE ts < ? AND ts % ? != 0 LIMIT 1",
		cutoff, downsampleStep,
	).Scan(&pending); err != nil {
		return err
	}
	if pending == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(
		`CREATE TEMP TABLE downsampled AS
		 SELECT (ts / ? * ?) AS ts, pod, namespace, app, env, AVG(cpu) AS cpu, CAST(AVG(memory) AS INTEGER) AS memory
		 FROM metrics WHERE ts < ?
		 GROUP BY (ts / ?), pod, namespace, app, env`,
		downsampleStep, downsampleStep, cutoff, downsampleStep,
	); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec("DELETE FROM metrics WHERE ts < ?", cutoff); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec("INSERT INTO metrics SELECT ts, pod, namespace, app, env, cpu, memory FROM downsampled"); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec("DROP TABLE downsampled"); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) chunkedDelete(table, where string, cutoff any) (int64, error) {
	query := fmt.Sprintf(
		"DELETE FROM %s WHERE rowid IN (SELECT rowid FROM %s WHERE %s LIMIT %d)",
		table, table, where, trimChunkSize,
	)
	var total int64
	for {
		res, err := s.db.Exec(query, cutoff)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
		if n < trimChunkSize {
			return total, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}
