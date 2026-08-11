package activity

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mortise-org/mortise/internal/constants"
)

// Cap is the ring-buffer size: the maximum number of events kept per project.
//
// The bound is the etcd object-size limit (1 MiB per ConfigMap, including
// metadata and managedFields). Measured marshaled entry sizes (see
// TestCapFitsEtcdBudget): a typical event is ~260 B and a worst-case entry
// (long message, several metadata pairs) ~600 B. 2000 × 600 B ≈ 1.15 MiB
// would not fit, so the count cap alone is not the guarantee — MaxBytes
// below is. 2000 × ~260 B ≈ 500 KiB of typical events sits comfortably
// under the byte budget; the count cap exists to keep pagination and
// full-list responses bounded and predictable.
const Cap = 2000

// MaxBytes is the hard budget for the marshaled events payload: 768 KiB,
// leaving ≥256 KiB of the 1 MiB etcd object limit for ConfigMap metadata,
// managedFields, and future keys. Append trims oldest entries until the
// payload fits, so pathological entry sizes degrade capacity, never writes.
const MaxBytes = 768 * 1024

// Retention is the time-based prune horizon: entries older than this are
// dropped on append. Thirty days of history comfortably exceeds what the
// UI rail and dashboards consume; the stdout audit stream (emitAudit)
// remains the unbounded system of record.
const Retention = 30 * 24 * time.Hour

const (
	configMapNamePrefix    = "activity-"
	eventsKey              = "events"
	maxConflictRetries     = 5
	initialConflictBackoff = 50 * time.Millisecond
)

// configMapName returns the ConfigMap name that stores activity for project.
func configMapName(project string) string {
	return configMapNamePrefix + project
}

// projectNamespace returns the control namespace name backing project. The
// activity ConfigMap is project-scoped (not env-scoped) — it lives in the
// control namespace alongside App CRDs.
func projectNamespace(project string) string {
	return constants.ControlNamespace(project)
}

// ConfigMapStore persists activity events in a per-project ConfigMap ring
// buffer, capped at Cap entries. Appends also emit a structured stdout audit
// line so external log pipelines remain authoritative.
type ConfigMapStore struct {
	Client client.Client
	// now is injectable for retention tests; nil means time.Now.
	now func() time.Time
}

// NewConfigMapStore returns a ConfigMapStore backed by c.
func NewConfigMapStore(c client.Client) *ConfigMapStore {
	return &ConfigMapStore{Client: c}
}

func (s *ConfigMapStore) nowTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

var _ Store = (*ConfigMapStore)(nil)

// Append writes e into the project's ring-buffer ConfigMap and emits an
// audit stdout line. If the project namespace does not yet exist (e.g.
// project is being torn down), Append logs a warning and returns nil so
// that callers are not blocked on eventual-consistency ordering.
func (s *ConfigMapStore) Append(ctx context.Context, e Event) error {
	emitAudit(e)

	backoff := initialConflictBackoff
	for attempt := 0; attempt < maxConflictRetries; attempt++ {
		err := s.appendOnce(ctx, e)
		if err == nil {
			return nil
		}
		if k8serrors.IsNotFound(err) {
			// Namespace missing — project is mid-teardown. Not an error
			// for the caller; stdout audit line already emitted above.
			slog.Warn("activity: project namespace missing, skipping ConfigMap write",
				"project", e.Project,
				"action", e.Action,
			)
			return nil
		}
		if !k8serrors.IsConflict(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return fmt.Errorf("activity: append gave up after %d conflict retries", maxConflictRetries)
}

// appendOnce performs one load-modify-write cycle. Returns IsConflict on
// a stale ResourceVersion so Append can retry.
func (s *ConfigMapStore) appendOnce(ctx context.Context, e Event) error {
	ns := projectNamespace(e.Project)
	name := configMapName(e.Project)

	var cm corev1.ConfigMap
	err := s.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &cm)
	if k8serrors.IsNotFound(err) {
		data, mErr := marshalEvents([]Event{e})
		if mErr != nil {
			return mErr
		}
		created := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "mortise",
					"mortise.dev/kind":             "activity",
				},
			},
			Data: map[string]string{eventsKey: data},
		}
		return s.Client.Create(ctx, created)
	}
	if err != nil {
		return err
	}

	events, err := unmarshalEvents(cm.Data[eventsKey])
	if err != nil {
		return err
	}
	// One-time backfill: entries from before pagination existed carry Seq 0.
	// Assign positions once so cursors are stable across the whole buffer.
	// Cheap (only fires while zero-seq entries exist) and idempotent.
	backfillSeq(events)
	e.Seq = nextSeq(events)
	// Retention prunes only pre-existing entries: the event being appended
	// is never dropped, whatever its stamp.
	events = trimRetention(events, s.nowTime())
	events = append(events, e)
	events = trimSize(events)
	data, err := marshalEvents(events)
	if err != nil {
		return err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[eventsKey] = data
	return s.Client.Update(ctx, &cm)
}

// backfillSeq assigns sequence numbers to legacy zero-seq entries by
// continuing the sequence seen so far in buffer order. For an all-legacy
// buffer that yields positions 1..n. In the mixed-history edge (an operator
// rollback appends zero-seq entries after seq-carrying ones, e.g.
// [5,6,0,0]), the zero-seq entries were appended later — so continuing past
// the running max ([5,6,7,8]) both preserves true recency order and makes
// duplicate seqs impossible, which positional assignment could mint
// ([2,3,0] must not become [2,3,3]).
func backfillSeq(events []Event) {
	var runningMax int64
	for i := range events {
		if events[i].Seq == 0 {
			events[i].Seq = runningMax + 1
		}
		if events[i].Seq > runningMax {
			runningMax = events[i].Seq
		}
	}
}

// nextSeq returns one past the highest seq in events (1 for an empty buffer).
func nextSeq(events []Event) int64 {
	var max int64
	for i := range events {
		if events[i].Seq > max {
			max = events[i].Seq
		}
	}
	return max + 1
}

// trimRetention drops entries older than the Retention horizon.
func trimRetention(events []Event, now time.Time) []Event {
	cutoff := now.Add(-Retention)
	firstKept := 0
	for firstKept < len(events) && events[firstKept].Timestamp.Before(cutoff) {
		firstKept++
	}
	return events[firstKept:]
}

// trimSize applies the count cap, then the byte budget. Byte-trim drops
// oldest-first until the marshaled payload fits MaxBytes, so entry size can
// degrade capacity but never block a write.
func trimSize(events []Event) []Event {
	if len(events) > Cap {
		events = events[len(events)-Cap:]
	}

	for len(events) > 1 {
		data, err := marshalEvents(events)
		if err != nil || len(data) <= MaxBytes {
			break
		}
		// Drop the oldest ~12.5% rather than one-at-a-time: re-marshaling
		// per dropped entry would be quadratic on pathological payloads.
		drop := len(events) / 8
		if drop == 0 {
			drop = 1
		}
		events = events[drop:]
	}
	return events
}

// List returns up to limit events for project, newest first. A missing
// ConfigMap yields an empty slice rather than an error because a project
// with no recorded activity is a valid steady state.
func (s *ConfigMapStore) List(ctx context.Context, project string, limit int) ([]Event, error) {
	if limit <= 0 || limit > Cap {
		limit = Cap
	}

	var cm corev1.ConfigMap
	err := s.Client.Get(ctx, types.NamespacedName{
		Namespace: projectNamespace(project),
		Name:      configMapName(project),
	}, &cm)
	if k8serrors.IsNotFound(err) {
		return []Event{}, nil
	}
	if err != nil {
		return nil, err
	}

	events, err := unmarshalEvents(cm.Data[eventsKey])
	if err != nil {
		return nil, err
	}

	reversed := make([]Event, 0, len(events))
	for i := len(events) - 1; i >= 0; i-- {
		reversed = append(reversed, events[i])
	}
	if len(reversed) > limit {
		reversed = reversed[:limit]
	}
	return reversed, nil
}

// ListPage returns up to limit events newest-first, starting strictly after
// cursor (an opaque value from a previous page; "" means from the newest).
// nextCursor is "" when the returned page reaches the oldest stored event.
// Cursors are seq-based: stable across appends and trims because seq is
// assigned once and never reused. A cursor older than the retention window
// simply yields an empty final page.
func (s *ConfigMapStore) ListPage(ctx context.Context, project string, limit int, cursor string) ([]Event, string, error) {
	if limit <= 0 || limit > Cap {
		limit = Cap
	}

	var before int64
	if cursor != "" {
		parsed, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || parsed <= 0 {
			return nil, "", fmt.Errorf("%w: %q", ErrBadCursor, cursor)
		}
		before = parsed
	}

	var cm corev1.ConfigMap
	err := s.Client.Get(ctx, types.NamespacedName{
		Namespace: projectNamespace(project),
		Name:      configMapName(project),
	}, &cm)
	if k8serrors.IsNotFound(err) {
		return []Event{}, "", nil
	}
	if err != nil {
		return nil, "", err
	}

	events, err := unmarshalEvents(cm.Data[eventsKey])
	if err != nil {
		return nil, "", err
	}
	// Reads never mutate the ConfigMap; compute cursor positions for any
	// legacy zero-seq entries in memory the same way the write path would.
	backfillSeq(events)

	page := make([]Event, 0, limit)
	oldestSeq := int64(0)
	if len(events) > 0 {
		oldestSeq = events[0].Seq
	}
	for i := len(events) - 1; i >= 0 && len(page) < limit; i-- {
		if before > 0 && events[i].Seq >= before {
			continue
		}
		page = append(page, events[i])
	}

	next := ""
	if n := len(page); n > 0 && page[n-1].Seq > oldestSeq {
		next = strconv.FormatInt(page[n-1].Seq, 10)
	}
	return page, next, nil
}

func marshalEvents(events []Event) (string, error) {
	b, err := json.Marshal(events)
	if err != nil {
		return "", fmt.Errorf("marshal events: %w", err)
	}
	return string(b), nil
}

func unmarshalEvents(raw string) ([]Event, error) {
	if raw == "" {
		return nil, nil
	}
	var events []Event
	if err := json.Unmarshal([]byte(raw), &events); err != nil {
		return nil, fmt.Errorf("unmarshal events: %w", err)
	}
	return events, nil
}

// emitAudit writes the event to stdout via slog as a single structured line
// so external log pipelines can scrape authoritative audit history.
func emitAudit(e Event) {
	attrs := []any{
		"ts", e.Timestamp.UTC().Format(time.RFC3339),
		"actor", e.Actor,
		"action", e.Action,
		"kind", e.ResourceKind,
		"resource", e.ResourceName,
		"project", e.Project,
	}
	if e.Message != "" {
		attrs = append(attrs, "msg", e.Message)
	}
	for k, v := range e.Metadata {
		attrs = append(attrs, k, v)
	}
	slog.Info("activity", attrs...)
}
