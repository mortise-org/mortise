/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"sync"

	"k8s.io/apimachinery/pkg/types"
)

// buildPhase is the in-memory lifecycle state of an async build goroutine.
type buildPhase string

const (
	buildPhaseRunning   buildPhase = "Running"
	buildPhaseSucceeded buildPhase = "Succeeded"
	buildPhaseFailed    buildPhase = "Failed"
)

// maxBuildLogLines is the maximum number of build log lines retained in memory.
const maxBuildLogLines = 1000

// maxBuildLogLineBytes caps the size of a single log line. Longer lines are
// truncated and suffixed with truncatedSuffix so the UI and ConfigMap payload
// don't blow up on pathological output (e.g. a single-line minified asset).
// Counts bytes, not runes — BuildKit output is ASCII-dominant and the ConfigMap
// size guard downstream is byte-based too.
const maxBuildLogLineBytes = 2048

// truncatedSuffix marks a line that was cut at maxBuildLogLineBytes. Uses the
// UTF-8 ellipsis so truncation is visually distinct from "..." that might
// appear organically in tool output.
const truncatedSuffix = "… [truncated]"

// buildTracker holds the state of a single asynchronous build. Only the
// goroutine that owns the tracker mutates its fields; callers read them under
// the mutex. Cancel aborts the build.
type buildTracker struct {
	mu           sync.Mutex
	revision     string
	phase        buildPhase
	image        string   // set on success
	digest       string   // set on success
	detectedPort int32    // set on success when build detects EXPOSE / Railpack port
	errMsg       string   // set on failure
	logs         []string // rolling buffer of build log lines
	cancel       func()
}

// snapshot returns a value-copy of the tracker's mutable state.
func (t *buildTracker) snapshot() (phase buildPhase, revision, image, digest, errMsg string, detectedPort int32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.phase, t.revision, t.image, t.digest, t.errMsg, t.detectedPort
}

// appendLog adds a line to the build log buffer, truncating lines longer
// than maxBuildLogLineBytes and trimming the buffer to maxBuildLogLines.
func (t *buildTracker) appendLog(line string) {
	if len(line) > maxBuildLogLineBytes {
		line = line[:maxBuildLogLineBytes] + truncatedSuffix
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.logs) >= maxBuildLogLines {
		t.logs = t.logs[1:]
	}
	t.logs = append(t.logs, line)
}

// snapshotLogs returns a copy of the current build log lines.
func (t *buildTracker) snapshotLogs() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, len(t.logs))
	copy(out, t.logs)
	return out
}

// snapshotLogsSince returns log lines from offset onward and the total count.
// If offset >= len(logs), the returned slice is empty.
func (t *buildTracker) snapshotLogsSince(offset int) ([]string, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	total := len(t.logs)
	if offset >= total {
		return []string{}, total
	}
	if offset < 0 {
		offset = 0
	}
	out := make([]string, total-offset)
	copy(out, t.logs[offset:])
	return out, total
}

// setSucceeded marks the build as succeeded with the resulting image, digest,
// and any auto-detected container port.
func (t *buildTracker) setSucceeded(image, digest string, detectedPort int32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.phase = buildPhaseSucceeded
	t.image = image
	t.digest = digest
	t.detectedPort = detectedPort
}

// setFailed marks the build as failed with the given error message.
func (t *buildTracker) setFailed(msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.phase = buildPhaseFailed
	t.errMsg = msg
}

// buildTrackerStore is a concurrency-safe map of active builds keyed by App.
// Operator restarts lose the map; that's acceptable because builds are
// idempotent (same revision → same image) and the next reconcile will
// re-launch.
type buildTrackerStore struct {
	trackers sync.Map // map[types.NamespacedName]*buildTracker
}

// get returns the tracker for the given App, or nil if none exists.
func (s *buildTrackerStore) get(key types.NamespacedName) *buildTracker {
	v, ok := s.trackers.Load(key)
	if !ok {
		return nil
	}
	return v.(*buildTracker)
}

// set stores the tracker under the given key.
func (s *buildTrackerStore) set(key types.NamespacedName, t *buildTracker) {
	s.trackers.Store(key, t)
}

// delete removes the tracker for the given App.
func (s *buildTrackerStore) delete(key types.NamespacedName) {
	s.trackers.Delete(key)
}

// GetBuildLogs returns the current build log lines for the given App, or nil
// if no build is in progress. Exported for use by the API layer.
func (s *buildTrackerStore) GetBuildLogs(key types.NamespacedName) []string {
	t := s.get(key)
	if t == nil {
		return nil
	}
	return t.snapshotLogs()
}

// GetBuildLogsSince returns build log lines from offset onward, plus the total
// line count. Returns (nil, 0) if no build exists for the key.
func (s *buildTrackerStore) GetBuildLogsSince(key types.NamespacedName, offset int) ([]string, int) {
	t := s.get(key)
	if t == nil {
		return nil, 0
	}
	return t.snapshotLogsSince(offset)
}
