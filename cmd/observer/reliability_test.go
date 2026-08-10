package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
)

// mo-5i4 reliability contract tests: every collector cycle leaves a heartbeat
// (success or failure), failures are gaps rather than silence, restarts
// resume instead of duplicating or losing data, and retention bounds storage.

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func lastTick(t *testing.T, store *Store, collector string) Tick {
	t.Helper()
	ticks, err := store.LastTicks()
	if err != nil {
		t.Fatalf("LastTicks: %v", err)
	}
	for _, tick := range ticks {
		if tick.Collector == collector {
			return tick
		}
	}
	t.Fatalf("no tick recorded for collector %q (got %+v)", collector, ticks)
	return Tick{}
}

func TestMetricsCollectorRecordsFailedTickOnNamespaceListError(t *testing.T) {
	cs := fake.NewClientset()
	cs.PrependReactor("list", "namespaces", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver down")
	})
	//lint:ignore SA1019 NewClientset requires --with-applyconfig codegen not used in this module
	mc := metricsfake.NewSimpleClientset()
	store := newTestStore(t)

	collector := NewMetricsCollector(cs, mc, store, NewLiveMetricsCache(time.Hour), NewHealthTracker(store, discardLogger()), time.Minute, discardLogger())
	collector.collect(context.Background())

	tick := lastTick(t, store, collectorMetrics)
	if tick.OK {
		t.Fatal("namespace-list failure must record a FAILED tick, not silence")
	}
	if tick.Error == "" {
		t.Error("failed tick must carry the error summary")
	}
}

func TestMetricsCollectorRecordsObservedEmptyTick(t *testing.T) {
	// Zero entries is data: the heartbeat distinguishes "looked, found
	// nothing" from "never looked".
	cs := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "pj-empty"}},
	)
	//lint:ignore SA1019 NewClientset requires --with-applyconfig codegen not used in this module
	mc := metricsfake.NewSimpleClientset()
	store := newTestStore(t)

	collector := NewMetricsCollector(cs, mc, store, NewLiveMetricsCache(time.Hour), NewHealthTracker(store, discardLogger()), time.Minute, discardLogger())
	collector.collect(context.Background())

	tick := lastTick(t, store, collectorMetrics)
	if !tick.OK {
		t.Fatalf("observed-empty cycle must record a successful tick, got error %q", tick.Error)
	}
	if tick.Items != 0 {
		t.Errorf("expected 0 items, got %d", tick.Items)
	}
}

func TestQueryCoverageMarksUnobservedBuckets(t *testing.T) {
	store := newTestStore(t)

	// Ticks at t=0 and t=120; nothing at t=60.
	for _, ts := range []int64{5, 125} {
		if err := store.InsertTick(Tick{Collector: collectorMetrics, Ts: ts, OK: true}); err != nil {
			t.Fatalf("InsertTick: %v", err)
		}
	}

	coverage, err := store.QueryCoverage(collectorMetrics, 0, 179, 60)
	if err != nil {
		t.Fatalf("QueryCoverage: %v", err)
	}
	want := [][2]int64{{0, 1}, {60, 0}, {120, 1}}
	if len(coverage) != len(want) {
		t.Fatalf("coverage = %v, want %v", coverage, want)
	}
	for i := range want {
		if coverage[i] != want[i] {
			t.Fatalf("coverage[%d] = %v, want %v", i, coverage[i], want[i])
		}
	}
}

func TestQueryCoverageFailedTickIsNotCoverage(t *testing.T) {
	store := newTestStore(t)
	if err := store.InsertTick(Tick{Collector: collectorMetrics, Ts: 10, OK: false, Error: "boom"}); err != nil {
		t.Fatalf("InsertTick: %v", err)
	}
	coverage, err := store.QueryCoverage(collectorMetrics, 0, 59, 60)
	if err != nil {
		t.Fatalf("QueryCoverage: %v", err)
	}
	if len(coverage) != 1 || coverage[0][1] != 0 {
		t.Fatalf("failed tick must not count as coverage, got %v", coverage)
	}
}

func TestLogTailerResumesFromStoredCursor(t *testing.T) {
	store := newTestStore(t)
	if err := store.InsertLog(LogEntry{
		Ts: "2026-08-10T10:00:05.300Z", Pod: "web-1", Namespace: "pj-demo-prod",
		App: "web", Env: "prod", Stream: "stdout", Line: "hello",
	}); err != nil {
		t.Fatalf("InsertLog: %v", err)
	}

	last, err := store.LastLogTimestamp("pj-demo-prod", "web-1")
	if err != nil {
		t.Fatalf("LastLogTimestamp: %v", err)
	}
	if last != "2026-08-10T10:00:05.300Z" {
		t.Fatalf("cursor = %q", last)
	}

	// The tailer truncates the cursor down to the second: bounded (≤1s)
	// re-read, never a skip.
	ts, err := time.Parse(time.RFC3339Nano, last)
	if err != nil {
		t.Fatalf("parse cursor: %v", err)
	}
	since := ts.Truncate(time.Second)
	if since.After(ts) {
		t.Fatal("resume point must not be after the last stored line")
	}
	if ts.Sub(since) > time.Second {
		t.Fatal("resume re-read window must be bounded by one second")
	}
}

func TestTrafficFlushRetainsBatchOnInsertFailure(t *testing.T) {
	store := newTestStore(t)
	cs := fake.NewClientset()
	tc := NewTrafficCollector(cs, store, NewLiveTrafficCache(time.Hour), NewHealthTracker(store, discardLogger()), time.Minute, 5*time.Second, "mortise-deps", discardLogger())

	// Accumulate one closed bucket, then break the store by closing it.
	key := accKey{appEnvKey: appEnvKey{namespace: "pj-d-prod", app: "web", env: "prod"}, bucket: 0}
	tc.accumulator[key] = &trafficBucket{requests: 3, status2xx: 3}
	store.Close()

	tc.flush()
	if len(tc.pending) != 1 {
		t.Fatalf("failed insert must retain the batch for retry, pending = %d", len(tc.pending))
	}

	// Recover the store; the next flush drains the pending batch.
	recovered := newTestStore(t)
	tc.store = recovered
	tc.health = NewHealthTracker(recovered, discardLogger())
	tc.flush()
	if len(tc.pending) != 0 {
		t.Fatalf("pending batch must drain after recovery, pending = %d", len(tc.pending))
	}
	var cnt int
	recovered.db.QueryRow("SELECT COUNT(*) FROM traffic").Scan(&cnt)
	if cnt != 1 {
		t.Fatalf("expected 1 traffic row after recovery, got %d", cnt)
	}
}

func TestDownsampleCollapsesOldMetrics(t *testing.T) {
	store := newTestStore(t)

	// 60 raw points, one per second, all older than the cutoff; plus one
	// recent point that must stay raw.
	old := make([]MetricEntry, 0, 60)
	for i := int64(0); i < 60; i++ {
		old = append(old, MetricEntry{
			Ts: 1000 + i, Pod: "web-1", Namespace: "pj-d-prod", App: "web", Env: "prod",
			CPU: 1.0, Memory: 100,
		})
	}
	if err := store.InsertMetrics(old); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}
	recent := MetricEntry{Ts: 5000, Pod: "web-1", Namespace: "pj-d-prod", App: "web", Env: "prod", CPU: 2.0, Memory: 200}
	if err := store.InsertMetrics([]MetricEntry{recent}); err != nil {
		t.Fatalf("InsertMetrics recent: %v", err)
	}

	if err := store.downsampleMetrics(2000); err != nil {
		t.Fatalf("downsampleMetrics: %v", err)
	}

	var oldRows, recentRows int
	store.db.QueryRow("SELECT COUNT(*) FROM metrics WHERE ts < 2000").Scan(&oldRows)
	store.db.QueryRow("SELECT COUNT(*) FROM metrics WHERE ts >= 2000").Scan(&recentRows)
	if oldRows != 1 {
		t.Fatalf("60 old raw points must collapse to 1 bucket row, got %d", oldRows)
	}
	if recentRows != 1 {
		t.Fatalf("recent point must stay raw, got %d rows", recentRows)
	}

	// Averages survive the collapse.
	var cpu float64
	store.db.QueryRow("SELECT cpu FROM metrics WHERE ts < 2000").Scan(&cpu)
	if cpu != 1.0 {
		t.Errorf("downsampled cpu = %f, want 1.0", cpu)
	}

	// Idempotent: a second pass with nothing finer than the bucket size
	// changes nothing.
	if err := store.downsampleMetrics(2000); err != nil {
		t.Fatalf("second downsample: %v", err)
	}
	var after int
	store.db.QueryRow("SELECT COUNT(*) FROM metrics").Scan(&after)
	if after != 2 {
		t.Fatalf("second pass must be a no-op, got %d rows", after)
	}
}

func TestTrimPrunesTicksAndPVC(t *testing.T) {
	store := newTestStore(t)
	oldTs := time.Now().Add(-100 * time.Hour).Unix()
	if err := store.InsertTick(Tick{Collector: collectorMetrics, Ts: oldTs, OK: true}); err != nil {
		t.Fatalf("InsertTick: %v", err)
	}
	if err := store.InsertPVCMetrics([]PVCEntry{{Ts: oldTs, Namespace: "pj-d-prod", App: "web", Env: "prod", PVC: "data", Capacity: 100, Used: 50}}); err != nil {
		t.Fatalf("InsertPVCMetrics: %v", err)
	}

	if _, _, err := store.Trim(72*time.Hour, 48*time.Hour, 48*time.Hour); err != nil {
		t.Fatalf("Trim: %v", err)
	}

	for _, table := range []string{"collector_ticks", "pvc_metrics"} {
		var cnt int
		store.db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&cnt)
		if cnt != 0 {
			t.Errorf("%s: expected old rows pruned, got %d", table, cnt)
		}
	}
}

func TestPVCCollectorDegradesGracefully(t *testing.T) {
	// Kubelet summary unavailable (managed clusters blocking nodes/proxy):
	// the collector records a failed tick and produces no series — metrics
	// absent, not erroring loudly.
	cs := fake.NewClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
	)
	store := newTestStore(t)
	c := NewPVCCollector(cs, store, NewHealthTracker(store, discardLogger()), time.Minute, discardLogger())
	c.summaryFn = func(context.Context, string) (*kubeletSummary, error) {
		return nil, errors.New("nodes/proxy blocked")
	}
	c.collect(context.Background())

	tick := lastTick(t, store, collectorPVC)
	if tick.OK {
		t.Fatal("unreachable kubelet summary must record a failed tick")
	}
	var cnt int
	store.db.QueryRow("SELECT COUNT(*) FROM pvc_metrics").Scan(&cnt)
	if cnt != 0 {
		t.Fatalf("expected no pvc rows, got %d", cnt)
	}
}

func TestPVCCollectorStoresLabeledSeries(t *testing.T) {
	cs := fake.NewClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: "db-0", Namespace: "pj-demo-prod",
			Labels: map[string]string{"app.kubernetes.io/name": "db", "mortise.dev/environment": "prod"},
		}},
	)
	store := newTestStore(t)
	c := NewPVCCollector(cs, store, NewHealthTracker(store, discardLogger()), time.Minute, discardLogger())
	c.summaryFn = func(context.Context, string) (*kubeletSummary, error) {
		var s kubeletSummary
		s.Pods = make([]kubeletSummaryPod, 1)
		s.Pods[0].PodRef.Name = "db-0"
		s.Pods[0].PodRef.Namespace = "pj-demo-prod"
		s.Pods[0].Volumes = []kubeletVolumeStats{{
			Name: "data", CapacityBytes: 10 << 30, UsedBytes: 4 << 30,
			PVCRef: &kubeletPVCRef{Name: "db-data", Namespace: "pj-demo-prod"},
		}}
		return &s, nil
	}
	c.collect(context.Background())

	tick := lastTick(t, store, collectorPVC)
	if !tick.OK || tick.Items != 1 {
		t.Fatalf("expected successful tick with 1 item, got ok=%v items=%d err=%q", tick.OK, tick.Items, tick.Error)
	}
	series, err := store.QueryPVCMetrics("pj-demo-prod", "db", "prod", 0, time.Now().Unix()+60, 60)
	if err != nil {
		t.Fatalf("QueryPVCMetrics: %v", err)
	}
	if len(series) != 1 || series[0].Name != "db-data" {
		t.Fatalf("series = %+v", series)
	}
	if series[0].Used[0][1] != float64(int64(4<<30)) {
		t.Errorf("used = %f", series[0].Used[0][1])
	}
}
