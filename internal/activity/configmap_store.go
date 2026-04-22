package activity

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mortise-org/mortise/internal/constants"
)

// Cap is the ring-buffer size: the maximum number of events kept per project.
const Cap = 500

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
}

// NewConfigMapStore returns a ConfigMapStore backed by c.
func NewConfigMapStore(c client.Client) *ConfigMapStore {
	return &ConfigMapStore{Client: c}
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
		if errors.IsNotFound(err) {
			// Namespace missing — project is mid-teardown. Not an error
			// for the caller; stdout audit line already emitted above.
			slog.Warn("activity: project namespace missing, skipping ConfigMap write",
				"project", e.Project,
				"action", e.Action,
			)
			return nil
		}
		if !errors.IsConflict(err) {
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
	if errors.IsNotFound(err) {
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
	events = append(events, e)
	if len(events) > Cap {
		events = events[len(events)-Cap:]
	}
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
	if errors.IsNotFound(err) {
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
