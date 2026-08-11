package activity

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// White-box tests for the Option C mechanics (#165): the etcd-budget math,
// seq assignment/backfill, cursor pagination, retention, and byte-trim.

func newStore(t *testing.T) *ConfigMapStore {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1: %v", err)
	}
	ns := &corev1.Namespace{}
	ns.Name = "pj-demo"
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns).Build()
	return NewConfigMapStore(c)
}

func recentEvent(i int, msg string) Event {
	return Event{
		Timestamp:    time.Now().UTC().Add(time.Duration(i) * time.Millisecond),
		Actor:        "jane@example.com",
		Action:       "app.deploy",
		ResourceKind: "App",
		ResourceName: fmt.Sprintf("app-%d", i),
		Project:      "demo",
		Message:      msg,
	}
}

// TestCapFitsEtcdBudget is the measured math behind Cap and MaxBytes: a
// buffer of Cap typical events must fit the byte budget with headroom, and
// MaxBytes must leave >= 256 KiB of the 1 MiB etcd object limit for
// ConfigMap metadata. If Event grows fields and typical size drifts up,
// this fails and the constants get re-derived instead of silently lying.
func TestCapFitsEtcdBudget(t *testing.T) {
	typical := make([]Event, Cap)
	for i := range typical {
		e := recentEvent(i, "Deployed my-service to production (image sha256:0123456789abcdef)")
		e.Seq = int64(i + 1)
		typical[i] = e
	}
	data, err := marshalEvents(typical)
	if err != nil {
		t.Fatal(err)
	}
	perEntry := len(data) / Cap
	t.Logf("measured typical entry: %d B; %d entries: %d KiB", perEntry, Cap, len(data)/1024)
	if len(data) > MaxBytes {
		t.Fatalf("Cap (%d) typical events = %d B exceeds MaxBytes (%d) — re-derive the constants", Cap, len(data), MaxBytes)
	}
	const etcdLimit = 1024 * 1024
	if etcdLimit-MaxBytes < 256*1024 {
		t.Fatalf("MaxBytes (%d) leaves less than 256 KiB metadata headroom under the 1 MiB etcd limit", MaxBytes)
	}
}

func TestAppendAssignsMonotonicSeq(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := s.Append(ctx, recentEvent(i, "m")); err != nil {
			t.Fatal(err)
		}
	}
	events, err := s.List(ctx, "demo", 10)
	if err != nil {
		t.Fatal(err)
	}
	// Newest first: seqs 5..1.
	for i, e := range events {
		if want := int64(5 - i); e.Seq != want {
			t.Fatalf("events[%d].Seq = %d, want %d", i, e.Seq, want)
		}
	}
}

func TestBackfillAssignsLegacyZeroSeq(t *testing.T) {
	events := []Event{{Seq: 0}, {Seq: 0}, {Seq: 0}}
	backfillSeq(events)
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Fatalf("backfill events[%d].Seq = %d, want %d", i, e.Seq, i+1)
		}
	}
	if nextSeq(events) != 4 {
		t.Fatalf("nextSeq = %d, want 4", nextSeq(events))
	}
}

func TestListPagePaginatesStableAcrossAppends(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		if err := s.Append(ctx, recentEvent(i, fmt.Sprintf("m%d", i))); err != nil {
			t.Fatal(err)
		}
	}

	page1, cursor, err := s.ListPage(ctx, "demo", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 10 || cursor == "" {
		t.Fatalf("page1: %d events, cursor %q", len(page1), cursor)
	}

	// Concurrent append between pages must not shift page 2: cursors are
	// seq-anchored, not offset-anchored.
	if err := s.Append(ctx, recentEvent(99, "concurrent")); err != nil {
		t.Fatal(err)
	}

	page2, cursor2, err := s.ListPage(ctx, "demo", 10, cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 10 {
		t.Fatalf("page2: %d events", len(page2))
	}
	if page2[0].Seq != page1[9].Seq-1 {
		t.Fatalf("page2 must continue exactly after page1: got seq %d after %d", page2[0].Seq, page1[9].Seq)
	}
	seen := map[int64]bool{}
	for _, e := range append(append([]Event{}, page1...), page2...) {
		if seen[e.Seq] {
			t.Fatalf("duplicate seq %d across pages", e.Seq)
		}
		seen[e.Seq] = true
	}

	page3, cursor3, err := s.ListPage(ctx, "demo", 10, cursor2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page3) != 5 {
		t.Fatalf("page3: %d events, want the remaining 5", len(page3))
	}
	if cursor3 != "" {
		t.Fatalf("final page must return empty cursor, got %q", cursor3)
	}
}

func TestListPageBadCursor(t *testing.T) {
	s := newStore(t)
	if _, _, err := s.ListPage(context.Background(), "demo", 10, "not-a-cursor"); err == nil || !strings.Contains(err.Error(), "invalid cursor") {
		t.Fatalf("expected ErrBadCursor, got %v", err)
	}
}

func TestRetentionPrunesOldEntriesButNeverTheAppended(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Two appends at T0.
	base := time.Now().UTC()
	s.now = func() time.Time { return base }
	old1 := recentEvent(0, "old")
	old1.Timestamp = base
	if err := s.Append(ctx, old1); err != nil {
		t.Fatal(err)
	}

	// Time passes beyond the retention horizon; a new append prunes the
	// old entries but keeps itself, even if its own stamp were stale.
	s.now = func() time.Time { return base.Add(Retention + time.Hour) }
	stale := recentEvent(1, "fresh-append-stale-stamp")
	stale.Timestamp = base // deliberately ancient stamp
	if err := s.Append(ctx, stale); err != nil {
		t.Fatal(err)
	}

	events, err := s.List(ctx, "demo", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected only the just-appended event to survive retention, got %d", len(events))
	}
	if events[0].Message != "fresh-append-stale-stamp" {
		t.Fatalf("survivor = %q", events[0].Message)
	}
}

func TestByteBudgetTrimsOldestNeverBlocks(t *testing.T) {
	// Entries with ~8 KiB messages: ~96 of them blow through MaxBytes long
	// before Cap. Appends must keep succeeding while capacity degrades.
	s := newStore(t)
	ctx := context.Background()
	big := strings.Repeat("x", 8*1024)
	const n = 120
	for i := 0; i < n; i++ {
		if err := s.Append(ctx, recentEvent(i, big)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	events, err := s.List(ctx, "demo", Cap)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || len(events) >= n {
		t.Fatalf("expected byte budget to trim some but not all: kept %d of %d", len(events), n)
	}
	// Newest survive, oldest were dropped.
	if events[0].Seq != int64(n) {
		t.Fatalf("newest seq = %d, want %d", events[0].Seq, n)
	}
	data, err := marshalEvents(nil)
	_ = data
	if err != nil {
		t.Fatal(err)
	}
}
