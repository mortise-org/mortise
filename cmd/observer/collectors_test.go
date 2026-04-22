package main

import (
	"context"
	"log/slog"
	"math"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(nopWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// --- isEnvNamespace ---

func TestIsEnvNamespace(t *testing.T) {
	// isEnvNamespace is only called after a HasPrefix("pj-") guard, so
	// non-pj names are out of contract. We test the cases that matter.
	tests := []struct {
		name string
		want bool
	}{
		{"pj-myproj-prod", true},
		{"pj-myproj-staging", true},
		{"pj-a-b", true},
		{"pj-foo-bar-baz", true},
		{"pj-myproj", false},
		{"pj-", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isEnvNamespace(tt.name); got != tt.want {
				t.Errorf("isEnvNamespace(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// --- parseLogTimestamp ---

func TestParseLogTimestamp(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		line := "2025-04-01T12:00:00.123456789Z hello world"
		ts, content := parseLogTimestamp(line)
		if ts != "2025-04-01T12:00:00.123456789Z" {
			t.Errorf("ts = %q, want 2025-04-01T12:00:00.123456789Z", ts)
		}
		if content != "hello world" {
			t.Errorf("content = %q, want %q", content, "hello world")
		}
	})

	t.Run("no space", func(t *testing.T) {
		ts, content := parseLogTimestamp("nospacehere")
		if content != "nospacehere" {
			t.Errorf("content = %q, want %q", content, "nospacehere")
		}
		if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
			t.Errorf("fallback ts should be valid RFC3339Nano, got %q: %v", ts, err)
		}
	})

	t.Run("invalid timestamp prefix", func(t *testing.T) {
		ts, content := parseLogTimestamp("notadate hello")
		if content != "notadate hello" {
			t.Errorf("content = %q, want %q", content, "notadate hello")
		}
		if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
			t.Errorf("fallback ts should be valid RFC3339Nano, got %q: %v", ts, err)
		}
	})

	t.Run("empty string", func(t *testing.T) {
		ts, content := parseLogTimestamp("")
		if content != "" {
			t.Errorf("content = %q, want empty", content)
		}
		if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
			t.Errorf("fallback ts should be valid RFC3339Nano, got %q: %v", ts, err)
		}
	})
}

// --- MetricsCollector.collect ---

// fakePodMetrics builds a metricsfake.Clientset whose PodMetricses List
// returns the given items. The standard fake tracker doesn't support
// PodMetrics objects, so we use a reactor.
func fakePodMetrics(items ...metricsv1beta1.PodMetrics) *metricsfake.Clientset {
	mc := metricsfake.NewSimpleClientset()
	mc.Fake.PrependReactor("list", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		la := action.(clienttesting.ListAction)
		ns := la.GetNamespace()
		var filtered []metricsv1beta1.PodMetrics
		for _, pm := range items {
			if ns == "" || pm.Namespace == ns {
				filtered = append(filtered, pm)
			}
		}
		return true, &metricsv1beta1.PodMetricsList{Items: filtered}, nil
	})
	return mc
}

func TestMetricsCollectorCollect(t *testing.T) {
	cs := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "pj-demo-prod"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "pj-demo"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
	)

	pm := metricsv1beta1.PodMetrics{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-pod-1",
			Namespace: "pj-demo-prod",
			Labels: map[string]string{
				"app.kubernetes.io/name":  "web",
				"mortise.dev/environment": "prod",
			},
		},
		Containers: []metricsv1beta1.ContainerMetrics{
			{
				Name: "app",
				Usage: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("250m"),
					corev1.ResourceMemory: resource.MustParse("128Mi"),
				},
			},
		},
	}
	mc := fakePodMetrics(pm)

	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	collector := NewMetricsCollector(cs, mc, store, time.Minute, discardLogger())

	collector.collect(context.Background())

	var cnt int
	store.db.QueryRow("SELECT COUNT(*) FROM metrics").Scan(&cnt)

	results, err := store.QueryMetrics("pj-demo-prod", "web", "prod", 0, time.Now().Unix()+60, 60)
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 pod series, got %d", len(results))
	}
	if results[0].Name != "web-pod-1" {
		t.Errorf("pod name = %q, want web-pod-1", results[0].Name)
	}
	if len(results[0].CPU) != 1 {
		t.Fatalf("expected 1 CPU data point, got %d", len(results[0].CPU))
	}
	if results[0].CPU[0][1] != 0.25 {
		t.Errorf("CPU = %f, want 0.25", results[0].CPU[0][1])
	}
	if results[0].Memory[0][1] != float64(128*1024*1024) {
		t.Errorf("Memory = %f, want %f", results[0].Memory[0][1], float64(128*1024*1024))
	}
}

func TestMetricsCollectorSkipsNonEnvNamespaces(t *testing.T) {
	cs := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "pj-demo"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
	)
	mc := metricsfake.NewSimpleClientset()

	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	collector := NewMetricsCollector(cs, mc, store, time.Minute, discardLogger())
	collector.collect(context.Background())

	results, err := store.QueryMetrics("pj-demo", "web", "prod", 0, time.Now().Unix()+60, 60)
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for control namespace, got %d", len(results))
	}
}

func TestMetricsCollectorSkipsUnlabeledPods(t *testing.T) {
	cs := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "pj-demo-prod"}},
	)
	mc := fakePodMetrics(metricsv1beta1.PodMetrics{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unlabeled-pod",
			Namespace: "pj-demo-prod",
		},
		Containers: []metricsv1beta1.ContainerMetrics{
			{
				Name: "app",
				Usage: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
			},
		},
	})

	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	collector := NewMetricsCollector(cs, mc, store, time.Minute, discardLogger())
	collector.collect(context.Background())

	results, err := store.QueryMetrics("pj-demo-prod", "", "", 0, time.Now().Unix()+60, 60)
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected unlabeled pod to be skipped, got %d results", len(results))
	}
}

func TestMetricsCollectorMultiContainer(t *testing.T) {
	cs := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "pj-demo-prod"}},
	)
	mc := fakePodMetrics(metricsv1beta1.PodMetrics{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "multi-pod",
			Namespace: "pj-demo-prod",
			Labels: map[string]string{
				"app.kubernetes.io/name":  "web",
				"mortise.dev/environment": "prod",
			},
		},
		Containers: []metricsv1beta1.ContainerMetrics{
			{
				Name: "app",
				Usage: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("200m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
			},
			{
				Name: "sidecar",
				Usage: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("32Mi"),
				},
			},
		},
	})

	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	collector := NewMetricsCollector(cs, mc, store, time.Minute, discardLogger())
	collector.collect(context.Background())

	results, err := store.QueryMetrics("pj-demo-prod", "web", "prod", 0, time.Now().Unix()+60, 60)
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 pod series, got %d", len(results))
	}
	if math.Abs(results[0].CPU[0][1]-0.3) > 1e-9 {
		t.Errorf("aggregated CPU = %f, want 0.3 (200m + 100m)", results[0].CPU[0][1])
	}
	wantMem := float64(96 * 1024 * 1024)
	if results[0].Memory[0][1] != wantMem {
		t.Errorf("aggregated Memory = %f, want %f (64Mi + 32Mi)", results[0].Memory[0][1], wantMem)
	}
}

// --- LogCollector ---

func TestLogCollectorSyncFindsEnvPods(t *testing.T) {
	cs := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "pj-demo-prod"}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "web-pod-1",
				Namespace: "pj-demo-prod",
				Labels: map[string]string{
					"app.kubernetes.io/name":  "web",
					"mortise.dev/environment": "prod",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25.0"}},
			},
		},
	)

	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	collector := NewLogCollector(cs, store, time.Minute, 100, discardLogger())
	collector.sync(context.Background())

	collector.mu.Lock()
	count := collector.tailerCount
	_, exists := collector.tailers["pj-demo-prod/web-pod-1"]
	collector.mu.Unlock()

	if count != 1 {
		t.Errorf("tailerCount = %d, want 1", count)
	}
	if !exists {
		t.Error("expected tailer for pj-demo-prod/web-pod-1")
	}
}

func TestLogCollectorMaxPods(t *testing.T) {
	cs := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "pj-demo-prod"}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-1",
				Namespace: "pj-demo-prod",
				Labels:    map[string]string{"app.kubernetes.io/name": "web"},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-2",
				Namespace: "pj-demo-prod",
				Labels:    map[string]string{"app.kubernetes.io/name": "web"},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-3",
				Namespace: "pj-demo-prod",
				Labels:    map[string]string{"app.kubernetes.io/name": "web"},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
		},
	)

	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	collector := NewLogCollector(cs, store, time.Minute, 2, discardLogger())
	collector.sync(context.Background())

	collector.mu.Lock()
	count := collector.tailerCount
	collector.mu.Unlock()

	if count != 2 {
		t.Errorf("tailerCount = %d, want 2 (maxPods enforced)", count)
	}
}

func TestLogCollectorSyncCleansUpRemovedPods(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-pod-1",
			Namespace: "pj-demo-prod",
			Labels:    map[string]string{"app.kubernetes.io/name": "web"},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
	}
	cs := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "pj-demo-prod"}},
		pod,
	)

	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	collector := NewLogCollector(cs, store, time.Minute, 100, discardLogger())
	collector.sync(context.Background())

	collector.mu.Lock()
	if collector.tailerCount != 1 {
		t.Fatalf("expected 1 tailer after first sync, got %d", collector.tailerCount)
	}
	collector.mu.Unlock()

	if err := cs.CoreV1().Pods("pj-demo-prod").Delete(context.Background(), "web-pod-1", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete pod: %v", err)
	}

	collector.sync(context.Background())

	collector.mu.Lock()
	count := collector.tailerCount
	collector.mu.Unlock()

	if count != 0 {
		t.Errorf("tailerCount = %d after pod removal, want 0", count)
	}
}

func TestLogCollectorSyncSkipsNonEnvNamespaces(t *testing.T) {
	cs := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "pj-demo"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "control-pod",
				Namespace: "pj-demo",
				Labels:    map[string]string{"app.kubernetes.io/name": "web"},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
		},
	)

	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	collector := NewLogCollector(cs, store, time.Minute, 100, discardLogger())
	collector.sync(context.Background())

	collector.mu.Lock()
	count := collector.tailerCount
	collector.mu.Unlock()

	if count != 0 {
		t.Errorf("tailerCount = %d, want 0 (control ns should be skipped)", count)
	}
}

func TestLogCollectorStartTailerIdempotent(t *testing.T) {
	cs := fake.NewClientset()

	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	collector := NewLogCollector(cs, store, time.Minute, 100, discardLogger())

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-1",
			Namespace: "pj-demo-prod",
			Labels: map[string]string{
				"app.kubernetes.io/name":  "web",
				"mortise.dev/environment": "prod",
			},
		},
	}

	ctx := context.Background()
	collector.startTailer(ctx, "pj-demo-prod", pod)
	collector.startTailer(ctx, "pj-demo-prod", pod)
	collector.startTailer(ctx, "pj-demo-prod", pod)

	collector.mu.Lock()
	count := collector.tailerCount
	collector.mu.Unlock()

	if count != 1 {
		t.Errorf("tailerCount = %d after 3 calls for same pod, want 1", count)
	}
}

func TestLogCollectorStopAll(t *testing.T) {
	cs := fake.NewClientset()

	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	collector := NewLogCollector(cs, store, time.Minute, 100, discardLogger())

	ctx := context.Background()
	for _, name := range []string{"pod-1", "pod-2", "pod-3"} {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "pj-demo-prod",
				Labels: map[string]string{
					"app.kubernetes.io/name":  "web",
					"mortise.dev/environment": "prod",
				},
			},
		}
		collector.startTailer(ctx, "pj-demo-prod", pod)
	}

	collector.mu.Lock()
	if collector.tailerCount != 3 {
		t.Fatalf("expected 3 tailers before stopAll, got %d", collector.tailerCount)
	}
	collector.mu.Unlock()

	collector.stopAll()

	collector.mu.Lock()
	for _, cancel := range collector.tailers {
		cancel()
	}
	collector.mu.Unlock()
}

// --- Run context cancellation ---

func TestMetricsCollectorRunStopsOnCancel(t *testing.T) {
	cs := fake.NewClientset()
	mc := metricsfake.NewSimpleClientset()

	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	collector := NewMetricsCollector(cs, mc, store, time.Hour, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		collector.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func TestLogCollectorRunStopsOnCancel(t *testing.T) {
	cs := fake.NewClientset()

	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	collector := NewLogCollector(cs, store, time.Hour, 100, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		collector.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}
