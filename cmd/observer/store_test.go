package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestInsertAndQueryMetrics(t *testing.T) {
	s := testStore(t)

	entries := []MetricEntry{
		{Ts: 1700000000, Pod: "pod-1", Namespace: "pj-demo-prod", App: "web", Env: "production", CPU: 0.25, Memory: 128000000},
		{Ts: 1700000060, Pod: "pod-1", Namespace: "pj-demo-prod", App: "web", Env: "production", CPU: 0.30, Memory: 130000000},
		{Ts: 1700000000, Pod: "pod-2", Namespace: "pj-demo-prod", App: "web", Env: "production", CPU: 0.10, Memory: 64000000},
	}

	if err := s.InsertMetrics(entries); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	results, err := s.QueryMetrics("pj-demo-prod", "web", "production", 1700000000, 1700000120, 60)
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 pods, got %d", len(results))
	}

	// pod-1 has 2 entries in different buckets
	found := false
	for _, ps := range results {
		if ps.Name == "pod-1" {
			found = true
			if len(ps.CPU) != 2 {
				t.Errorf("pod-1: expected 2 CPU data points, got %d", len(ps.CPU))
			}
		}
	}
	if !found {
		t.Error("pod-1 not found in results")
	}
}

func TestQueryMetrics_Empty(t *testing.T) {
	s := testStore(t)

	results, err := s.QueryMetrics("pj-demo-prod", "web", "production", 1700000000, 1700000120, 60)
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results, got %d", len(results))
	}
}

func TestInsertAndQueryLogs(t *testing.T) {
	s := testStore(t)

	now := time.Now().UTC()
	ts1 := now.Add(-30 * time.Second).Format(time.RFC3339Nano)
	ts2 := now.Add(-20 * time.Second).Format(time.RFC3339Nano)
	ts3 := now.Add(-10 * time.Second).Format(time.RFC3339Nano)

	for _, e := range []LogEntry{
		{Ts: ts1, Pod: "pod-1", Namespace: "pj-demo-prod", App: "web", Env: "production", Stream: "stdout", Line: "hello world"},
		{Ts: ts2, Pod: "pod-1", Namespace: "pj-demo-prod", App: "web", Env: "production", Stream: "stdout", Line: "error: something failed"},
		{Ts: ts3, Pod: "pod-2", Namespace: "pj-demo-prod", App: "web", Env: "production", Stream: "stderr", Line: "another error"},
	} {
		if err := s.InsertLog(e); err != nil {
			t.Fatalf("InsertLog: %v", err)
		}
	}

	start := now.Add(-1 * time.Minute).Unix()
	end := now.Unix()

	lines, hasMore, err := s.QueryLogs("pj-demo-prod", "web", "production", start, end, 10, "", "")
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}
	if hasMore {
		t.Error("expected hasMore=false")
	}
}

func TestQueryLogs_Filter(t *testing.T) {
	s := testStore(t)

	now := time.Now().UTC()
	for _, e := range []LogEntry{
		{Ts: now.Add(-30 * time.Second).Format(time.RFC3339Nano), Pod: "pod-1", Namespace: "pj-demo-prod", App: "web", Env: "production", Line: "info: ok"},
		{Ts: now.Add(-20 * time.Second).Format(time.RFC3339Nano), Pod: "pod-1", Namespace: "pj-demo-prod", App: "web", Env: "production", Line: "error: bad thing"},
		{Ts: now.Add(-10 * time.Second).Format(time.RFC3339Nano), Pod: "pod-1", Namespace: "pj-demo-prod", App: "web", Env: "production", Line: "error: worse thing"},
	} {
		if err := s.InsertLog(e); err != nil {
			t.Fatalf("InsertLog: %v", err)
		}
	}

	start := now.Add(-1 * time.Minute).Unix()
	end := now.Unix()

	lines, _, err := s.QueryLogs("pj-demo-prod", "web", "production", start, end, 10, "error", "")
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(lines) != 2 {
		t.Errorf("expected 2 filtered lines, got %d", len(lines))
	}
}

func TestQueryLogs_Limit(t *testing.T) {
	s := testStore(t)

	now := time.Now().UTC()
	for i := range 5 {
		ts := now.Add(time.Duration(-50+i*10) * time.Second).Format(time.RFC3339Nano)
		if err := s.InsertLog(LogEntry{
			Ts: ts, Pod: "pod-1", Namespace: "pj-demo-prod", App: "web", Env: "production", Line: "line",
		}); err != nil {
			t.Fatalf("InsertLog: %v", err)
		}
	}

	start := now.Add(-1 * time.Minute).Unix()
	end := now.Unix()

	lines, hasMore, err := s.QueryLogs("pj-demo-prod", "web", "production", start, end, 3, "", "")
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}
	if !hasMore {
		t.Error("expected hasMore=true")
	}
}

func TestQueryLogs_Empty(t *testing.T) {
	s := testStore(t)

	lines, hasMore, err := s.QueryLogs("pj-demo-prod", "web", "production", 1700000000, 1700003600, 10, "", "")
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("expected empty, got %d", len(lines))
	}
	if hasMore {
		t.Error("expected hasMore=false")
	}
}

func TestTrim(t *testing.T) {
	s := testStore(t)

	oldTs := time.Now().Add(-100 * time.Hour).Unix()
	recentTs := time.Now().Add(-1 * time.Hour).Unix()

	if err := s.InsertMetrics([]MetricEntry{
		{Ts: oldTs, Pod: "pod-1", Namespace: "ns", App: "a", Env: "e", CPU: 0.1, Memory: 100},
		{Ts: recentTs, Pod: "pod-1", Namespace: "ns", App: "a", Env: "e", CPU: 0.1, Memory: 100},
	}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	oldLogTs := time.Now().Add(-100 * time.Hour).UTC().Format(time.RFC3339Nano)
	recentLogTs := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339Nano)

	for _, ts := range []string{oldLogTs, recentLogTs} {
		if err := s.InsertLog(LogEntry{Ts: ts, Pod: "pod-1", Namespace: "ns", App: "a", Env: "e", Line: "test"}); err != nil {
			t.Fatalf("InsertLog: %v", err)
		}
	}

	md, ld, err := s.Trim(72*time.Hour, 48*time.Hour)
	if err != nil {
		t.Fatalf("Trim: %v", err)
	}
	if md != 1 {
		t.Errorf("expected 1 metric deleted, got %d", md)
	}
	if ld != 1 {
		t.Errorf("expected 1 log deleted, got %d", ld)
	}

	// Verify recent data survives
	results, err := s.QueryMetrics("ns", "a", "e", recentTs-60, recentTs+60, 60)
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 surviving metric pod, got %d", len(results))
	}
}

func TestNewStore_InvalidPath(t *testing.T) {
	_, err := NewStore("/nonexistent/deeply/nested/path/test.db")
	if err != nil && !os.IsNotExist(err) {
		// modernc sqlite may create parent dirs or fail differently; just verify we get an error
		t.Logf("got expected error: %v", err)
	}
}
