package activity

import "time"

// Event is a single project-scoped audit entry. Fields mirror SPEC §5.11.
type Event struct {
	Timestamp    time.Time         `json:"ts"`
	Actor        string            `json:"actor"`
	Action       string            `json:"action"`
	ResourceKind string            `json:"kind"`
	ResourceName string            `json:"resource"`
	Project      string            `json:"project"`
	Message      string            `json:"msg"`
	Metadata     map[string]string `json:"meta,omitempty"`
}
