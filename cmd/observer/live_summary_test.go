package main

import (
	"testing"
	"time"
)

// LatestByApp feeds /v1/summary and the cluster dashboard's usage columns.
// The stale-cutoff boundary is the honesty contract: a pod whose series went
// quiet must drop out of the rollup, never linger as a phantom number.
func TestLatestByApp(t *testing.T) {
	now := time.Now().Unix()
	cutoff := now - 300

	cache := NewLiveMetricsCache(2 * time.Hour)
	// Collectors append in time order (each cycle stamps now), so an older
	// point for web-a lands before its fresh one — the invariant
	// LatestByApp's points[len-1] read relies on.
	cache.Add([]MetricEntry{
		{Ts: cutoff - 120, Namespace: "pj-shop-prod", App: "web", Env: "prod", Pod: "web-a", CPU: 99, Memory: 9999},
	})
	cache.Add([]MetricEntry{
		// Two fresh pods of the same app-env: summed, pod count 2.
		{Ts: now - 10, Namespace: "pj-shop-prod", App: "web", Env: "prod", Pod: "web-a", CPU: 0.25, Memory: 100},
		{Ts: now - 5, Namespace: "pj-shop-prod", App: "web", Env: "prod", Pod: "web-b", CPU: 0.5, Memory: 200},
		// Same app, second env: its own row.
		{Ts: now - 5, Namespace: "pj-shop-stg", App: "web", Env: "staging", Pod: "web-s", CPU: 0.1, Memory: 50},
		// Stale pod: latest point older than the cutoff — must not appear.
		{Ts: cutoff - 60, Namespace: "pj-shop-prod", App: "db", Env: "prod", Pod: "db-old", CPU: 9, Memory: 999},
		// Boundary: latest point exactly AT the cutoff is not stale.
		{Ts: cutoff, Namespace: "pj-shop-prod", App: "cache", Env: "prod", Pod: "cache-a", CPU: 0.05, Memory: 10},
	})
	got := cache.LatestByApp(cutoff)

	byKey := map[string]AppUsage{}
	for _, u := range got {
		byKey[u.Namespace+"/"+u.App+"/"+u.Env] = u
	}

	if len(got) != 3 {
		t.Fatalf("expected 3 rows (web/prod, web/staging, cache/prod), got %d: %+v", len(got), got)
	}
	if _, ok := byKey["pj-shop-prod/db/prod"]; ok {
		t.Fatal("stale series must drop out of the summary")
	}

	web := byKey["pj-shop-prod/web/prod"]
	if web.Pods != 2 || web.CPU != 0.75 || web.Memory != 300 {
		t.Fatalf("web/prod aggregation = %+v, want pods=2 cpu=0.75 mem=300", web)
	}
	if stg := byKey["pj-shop-stg/web/staging"]; stg.Pods != 1 || stg.CPU != 0.1 {
		t.Fatalf("web/staging = %+v", stg)
	}
	if c := byKey["pj-shop-prod/cache/prod"]; c.Pods != 1 {
		t.Fatalf("boundary-at-cutoff series must be included, got %+v", c)
	}

	// Deterministic ordering: sorted by namespace, app, env.
	for i := 1; i < len(got); i++ {
		a, b := got[i-1], got[i]
		if a.Namespace > b.Namespace || (a.Namespace == b.Namespace && a.App > b.App) {
			t.Fatalf("rows not sorted: %+v before %+v", a, b)
		}
	}
}

func TestLatestByAppEmptyAndNil(t *testing.T) {
	var nilCache *LiveMetricsCache
	if got := nilCache.LatestByApp(0); got != nil {
		t.Fatalf("nil cache must return nil, got %v", got)
	}
	if got := NewLiveMetricsCache(time.Hour).LatestByApp(0); len(got) != 0 {
		t.Fatalf("empty cache must return no rows, got %v", got)
	}
}
