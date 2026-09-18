package api

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/mortise-org/mortise/internal/constants"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// drainPodWatch debounces pod events into a dirty set. The flush must run on
// the loop goroutine: a timer callback iterating the map while the loop
// writes it is a fatal runtime error (not a recoverable panic) that took the
// operator down mid-E2E. Run with -race; the old shape reports a data race.
func TestDrainPodWatchFlushesOnLoopGoroutine(t *testing.T) {
	s := &Server{client: fake.NewClientBuilder().Build()}
	rec := httptest.NewRecorder()
	w := &sseWriter{w: rec, flusher: rec}
	fw := watch.NewFake()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.drainPodWatch(ctx, fw, "proj", "pj-proj-production", w)
	}()

	pod := func() *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name:      "p",
			Namespace: "pj-proj-production",
			Labels:    map[string]string{constants.AppNameLabel: "web", constants.EnvironmentLabel: "production"},
		}}
	}
	// Straddle several debounce windows so flushes interleave with writes.
	for i := 0; i < 8; i++ {
		fw.Modify(pod())
		time.Sleep(550 * time.Millisecond)
		fw.Modify(pod())
		time.Sleep(20 * time.Millisecond)
	}
	fw.Stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drainPodWatch did not return after the watch closed")
	}
}
