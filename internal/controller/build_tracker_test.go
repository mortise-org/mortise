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
	"strings"
	"testing"
)

func TestAppendLog_TruncatesOversizedLine(t *testing.T) {
	tr := &buildTracker{}
	long := strings.Repeat("x", maxBuildLogLineBytes+500)
	tr.appendLog(long)

	logs := tr.snapshotLogs()
	if len(logs) != 1 {
		t.Fatalf("expected 1 line, got %d", len(logs))
	}
	got := logs[0]
	if !strings.HasSuffix(got, truncatedSuffix) {
		t.Errorf("truncated line should end with %q, got %q", truncatedSuffix, got[len(got)-32:])
	}
	wantLen := maxBuildLogLineBytes + len(truncatedSuffix)
	if len(got) != wantLen {
		t.Errorf("expected truncated length %d, got %d", wantLen, len(got))
	}
}

func TestAppendLog_PreservesShortLines(t *testing.T) {
	tr := &buildTracker{}
	short := "step 1/5: COPY . /app"
	tr.appendLog(short)
	logs := tr.snapshotLogs()
	if len(logs) != 1 || logs[0] != short {
		t.Fatalf("short line altered: %v", logs)
	}
}

func TestAppendLog_RingBufferTrimsToCap(t *testing.T) {
	tr := &buildTracker{}
	for i := 0; i < maxBuildLogLines+50; i++ {
		tr.appendLog("line")
	}
	logs := tr.snapshotLogs()
	if len(logs) != maxBuildLogLines {
		t.Errorf("expected buffer capped at %d, got %d", maxBuildLogLines, len(logs))
	}
}

func TestAppendLog_RingBufferDropsOldestFirst(t *testing.T) {
	tr := &buildTracker{}
	// Fill with distinct markers so we can assert which ones survived.
	for i := 0; i < maxBuildLogLines; i++ {
		tr.appendLog("old")
	}
	for i := 0; i < 10; i++ {
		tr.appendLog("new")
	}
	logs := tr.snapshotLogs()
	if len(logs) != maxBuildLogLines {
		t.Fatalf("expected %d lines, got %d", maxBuildLogLines, len(logs))
	}
	// The last 10 should be "new", the rest "old".
	for i := maxBuildLogLines - 10; i < maxBuildLogLines; i++ {
		if logs[i] != "new" {
			t.Errorf("expected logs[%d] == %q, got %q", i, "new", logs[i])
		}
	}
	if logs[0] != "old" {
		t.Errorf("expected logs[0] == %q, got %q", "old", logs[0])
	}
}
