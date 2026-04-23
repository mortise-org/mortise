package main

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func testServer(t *testing.T) *ObserverServer {
	t.Helper()
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return NewObserverServer(store, NewLiveMetricsCache(2*time.Hour), NewLiveTrafficCache(2*time.Hour))
}

func TestHandleMetrics_MissingParams(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest("GET", "/v1/metrics", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleMetrics_ValidQuery(t *testing.T) {
	srv := testServer(t)

	// Insert some data
	entries := []MetricEntry{
		{Ts: 1700000000, Pod: "pod-1", Namespace: "pj-demo-prod", App: "web", Env: "production", CPU: 0.25, Memory: 128000000},
	}
	if err := srv.store.InsertMetrics(entries); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	req := httptest.NewRequest("GET", "/v1/metrics?namespace=pj-demo-prod&app=web&env=production&start=1700000000&end=1700003600&step=60", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var result struct {
		Pods []PodMetricsSeries `json:"pods"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Pods) != 1 {
		t.Errorf("expected 1 pod, got %d", len(result.Pods))
	}
}

func TestHandleLogs_MissingParams(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest("GET", "/v1/logs?namespace=ns&app=a", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleLogs_ValidQuery(t *testing.T) {
	srv := testServer(t)

	now := time.Now().UTC()
	ts := now.Add(-30 * time.Second).Format(time.RFC3339Nano)
	if err := srv.store.InsertLog(LogEntry{
		Ts: ts, Pod: "pod-1", Namespace: "pj-demo-prod", App: "web", Env: "production", Stream: "stdout", Line: "hello",
	}); err != nil {
		t.Fatalf("InsertLog: %v", err)
	}

	start := now.Add(-1 * time.Minute).Unix()
	end := now.Unix()

	req := httptest.NewRequest("GET", "/v1/logs?namespace=pj-demo-prod&app=web&env=production&start="+
		itoa(start)+"&end="+itoa(end), nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var result struct {
		Lines   []LogLine `json:"lines"`
		HasMore bool      `json:"hasMore"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Lines) != 1 {
		t.Errorf("expected 1 line, got %d", len(result.Lines))
	}
	if result.HasMore {
		t.Error("expected hasMore=false")
	}
}

func TestHandleHealthz(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
