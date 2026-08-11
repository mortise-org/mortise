package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The /metrics exposition is public API (SPEC §5.11b): these tests pin the
// format (HELP/TYPE per family, escaping, deterministic ordering) and the
// label contract {project, app, environment}.

func promTestState(t *testing.T) (*promState, *HealthTracker) {
	t.Helper()
	store := newTestStore(t)
	return newPromState(), NewHealthTracker(store, discardLogger())
}

func TestRenderMetricsResourceSeries(t *testing.T) {
	state, health := promTestState(t)
	state.SetResource("shop", "web", "production", "web-abc", 0.25, 128<<20, 3)
	health.Record(collectorMetrics, 1, nil)

	out := renderMetrics(state, health)

	for _, want := range []string{
		"# HELP mortise_app_cpu_cores ",
		"# TYPE mortise_app_cpu_cores gauge",
		`mortise_app_cpu_cores{project="shop",app="web",environment="production",pod="web-abc"} 0.25`,
		`mortise_app_memory_bytes{project="shop",app="web",environment="production",pod="web-abc"} 1.34217728e+08`,
		"# TYPE mortise_app_pod_restarts_total counter",
		`mortise_app_pod_restarts_total{project="shop",app="web",environment="production",pod="web-abc"} 3`,
		"# TYPE mortise_observer_collector_up gauge",
		`mortise_observer_collector_up{collector="metrics"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exposition missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderMetricsTrafficCounters(t *testing.T) {
	state, health := promTestState(t)
	e := TrafficEntry{Requests: 10, Status2xx: 8, Status4xx: 2, BytesIn: 100, BytesOut: 2000, LatencyP50: 12.5, LatencyP95: 80, LatencyP99: 200}
	state.AddTraffic("shop", "web", "production", e)
	state.AddTraffic("shop", "web", "production", e) // counters accumulate

	out := renderMetrics(state, health)

	for _, want := range []string{
		"# TYPE mortise_app_http_requests_total counter",
		`mortise_app_http_requests_total{project="shop",app="web",environment="production"} 20`,
		`mortise_app_http_responses_total{project="shop",app="web",environment="production",class="2xx"} 16`,
		`mortise_app_http_responses_total{project="shop",app="web",environment="production",class="4xx"} 4`,
		`mortise_app_http_request_bytes_total{project="shop",app="web",environment="production"} 200`,
		// Quantiles are gauges of the latest bucket, in seconds.
		`mortise_app_http_latency_seconds{project="shop",app="web",environment="production",quantile="0.5"} 0.0125`,
		`mortise_app_http_latency_seconds{project="shop",app="web",environment="production",quantile="0.99"} 0.2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exposition missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderMetricsPVCSeriesAndEscaping(t *testing.T) {
	state, health := promTestState(t)
	state.SetPVC("shop", "db", "production", `data"vol`, 10<<30, 4<<30)

	out := renderMetrics(state, health)

	if !strings.Contains(out, `mortise_app_pvc_used_bytes{project="shop",app="db",environment="production",pvc="data\"vol"} 4.294967296e+09`) {
		t.Errorf("pvc series with escaped label missing:\n%s", out)
	}
}

func TestRenderMetricsSweepsStaleSeries(t *testing.T) {
	state, health := promTestState(t)
	state.SetResource("shop", "web", "production", "web-old", 1, 1, 0)
	// Backdate the sample past the staleness horizon.
	state.mu.Lock()
	s := state.resources[resourceKey{"shop", "web", "production", "web-old"}]
	s.seen = time.Now().Add(-promStaleAfter - time.Minute)
	state.resources[resourceKey{"shop", "web", "production", "web-old"}] = s
	state.mu.Unlock()

	out := renderMetrics(state, health)
	if strings.Contains(out, "web-old") {
		t.Errorf("stale series must drop out of the exposition:\n%s", out)
	}
}

func TestRenderMetricsDeterministicOrdering(t *testing.T) {
	state, health := promTestState(t)
	state.SetResource("shop", "web", "production", "b-pod", 1, 1, 0)
	state.SetResource("shop", "web", "production", "a-pod", 1, 1, 0)

	first := renderMetrics(state, health)
	second := renderMetrics(state, health)
	if first != second {
		t.Error("exposition must be deterministic across renders")
	}
	if strings.Index(first, "a-pod") > strings.Index(first, "b-pod") {
		t.Error("series must be emitted in sorted order")
	}
}

func TestPromMetricsEndpoint(t *testing.T) {
	store := newTestStore(t)
	prom := newPromState()
	prom.SetResource("shop", "web", "production", "web-abc", 0.5, 1024, 0)
	srv := NewObserverServer(store, NewLiveMetricsCache(time.Hour), NewLiveTrafficCache(time.Hour), NewHealthTracker(store, discardLogger()), prom)

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("content type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "mortise_app_cpu_cores") {
		t.Error("endpoint must serve the exposition")
	}
}
