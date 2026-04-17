package activity

import "context"

// Store is the per-project append-only activity event store. Backed by a
// ConfigMap ring buffer in the project namespace (see SPEC §5.11).
type Store interface {
	// Append adds an event to the project's ring buffer and emits a
	// stdout audit line. Never returns an error for "ConfigMap not
	// found" — logs a warning and continues (project being torn down
	// is not an error state for the caller).
	Append(ctx context.Context, e Event) error

	// List returns the most recent N events in reverse chronological
	// order (newest first). Caller passes the project name. If
	// limit <= 0 or > Cap, Cap is used.
	List(ctx context.Context, project string, limit int) ([]Event, error)
}
