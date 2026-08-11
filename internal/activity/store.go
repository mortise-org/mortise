package activity

import (
	"context"
	"errors"
)

// ErrBadCursor is returned by ListPage for a cursor that is not one this
// store produced. Handlers map it to a 400.
var ErrBadCursor = errors.New("activity: invalid cursor")

// Store is the per-project append-only activity event store. Backed by a
// ConfigMap ring buffer in the project namespace (see SPEC §5.11). The
// interface is deliberately narrow so a future SQLite/observer-backed
// implementation (#165 Option A) is a drop-in swap.
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

	// ListPage is List with cursor pagination: it returns up to limit
	// events newest-first strictly after cursor ("" = from the newest)
	// plus the cursor for the next page ("" = no more pages). Cursors
	// are opaque to callers and stable across concurrent appends.
	ListPage(ctx context.Context, project string, limit int, cursor string) ([]Event, string, error)
}
