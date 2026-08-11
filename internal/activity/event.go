package activity

import "time"

// Event is a single project-scoped audit entry. Fields mirror SPEC §5.11.
type Event struct {
	// Seq is a per-project monotonically increasing sequence number assigned
	// at append time. It makes pagination cursors stable when timestamps
	// collide. Entries written before pagination existed carry Seq 0 until
	// the one-time backfill in appendOnce assigns them positions.
	Seq          int64             `json:"seq,omitempty"`
	Timestamp    time.Time         `json:"ts"`
	Actor        string            `json:"actor"`
	Action       string            `json:"action"`
	ResourceKind string            `json:"kind"`
	ResourceName string            `json:"resource"`
	Project      string            `json:"project"`
	Message      string            `json:"msg"`
	Metadata     map[string]string `json:"meta,omitempty"`
}
