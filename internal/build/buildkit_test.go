package build

import (
	"context"
	"errors"
	"testing"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	exptypes "github.com/moby/buildkit/exporter/containerimage/exptypes"
)

// TestSubmit_ValidationErrors ensures Submit returns errors for missing fields.
func TestSubmit_ValidationErrors(t *testing.T) {
	c := newWithSolver(Config{Addr: "tcp://localhost:1234"}, &fakeSolverImpl{})

	_, err := c.Submit(context.Background(), BuildRequest{PushTarget: "registry/img:tag"})
	if err == nil {
		t.Fatal("expected error for empty SourceDir")
	}

	_, err = c.Submit(context.Background(), BuildRequest{SourceDir: "/tmp/src"})
	if err == nil {
		t.Fatal("expected error for empty PushTarget")
	}
}

// TestSubmit_Success verifies that a successful Solve emits log lines and a
// success event with the image digest.
func TestSubmit_Success(t *testing.T) {
	const wantDigest = "sha256:abc123"

	fs := &fakeSolverImpl{
		resp: &bkclient.SolveResponse{
			ExporterResponse: map[string]string{
				exptypes.ExporterImageDigestKey: wantDigest,
			},
		},
		logs: []string{"Step 1/3 : FROM alpine", "Step 2/3 : RUN echo hello"},
	}
	c := newWithSolver(Config{Addr: "tcp://localhost:1234"}, fs)

	ch, err := c.Submit(context.Background(), BuildRequest{
		SourceDir:  "/tmp/src",
		PushTarget: "registry/img:tag",
	})
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}

	var logLines []string
	var lastEvent BuildEvent
	for ev := range ch {
		lastEvent = ev
		if ev.Type == EventLog {
			logLines = append(logLines, ev.Line)
		}
	}

	if lastEvent.Type != EventSuccess {
		t.Fatalf("last event type = %q, want %q", lastEvent.Type, EventSuccess)
	}
	if lastEvent.Digest != wantDigest {
		t.Fatalf("digest = %q, want %q", lastEvent.Digest, wantDigest)
	}
	if len(logLines) != 2 {
		t.Fatalf("got %d log lines, want 2; lines: %v", len(logLines), logLines)
	}
}

// TestSubmit_BuildFailure verifies that a Solve error is surfaced as an
// EventFailure event and no EventSuccess is emitted.
func TestSubmit_BuildFailure(t *testing.T) {
	fs := &fakeSolverImpl{err: errors.New("exit code: 1")}
	c := newWithSolver(Config{Addr: "tcp://localhost:1234"}, fs)

	ch, err := c.Submit(context.Background(), BuildRequest{
		SourceDir:  "/tmp/src",
		PushTarget: "registry/img:tag",
	})
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}

	var lastEvent BuildEvent
	for ev := range ch {
		lastEvent = ev
	}
	if lastEvent.Type != EventFailure {
		t.Fatalf("last event type = %q, want %q", lastEvent.Type, EventFailure)
	}
	if lastEvent.Error == "" {
		t.Fatal("expected non-empty error string on failure event")
	}
}

// TestSubmit_ContextCancellation verifies that cancelling ctx causes the build
// goroutine to exit and the channel to be closed.
func TestSubmit_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	fs := &fakeSolverImpl{
		// Cancel before returning so the Solve call appears to honour cancellation.
		sideEffect: cancel,
		err:        context.Canceled,
	}
	c := newWithSolver(Config{Addr: "tcp://localhost:1234"}, fs)

	ch, err := c.Submit(ctx, BuildRequest{
		SourceDir:  "/tmp/src",
		PushTarget: "registry/img:tag",
	})
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}

	// Drain the channel — it must close (not block).
	var events []BuildEvent
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) == 0 {
		t.Fatal("expected at least a failure event after cancellation")
	}
	last := events[len(events)-1]
	if last.Type != EventFailure {
		t.Fatalf("last event = %q, want %q", last.Type, EventFailure)
	}
}

// TestNew_EmptyAddr verifies that New rejects a missing address.
func TestNew_EmptyAddr(t *testing.T) {
	_, err := New(Config{})
	if err == nil {
		t.Fatal("expected error for empty addr")
	}
}

// TestNew_TLSCertMissingKey verifies that New rejects TLSCert without TLSKey.
func TestNew_TLSCertMissingKey(t *testing.T) {
	_, err := New(Config{Addr: "tcp://localhost:1234", TLSCert: "/path/to/cert"})
	if err == nil {
		t.Fatal("expected error for TLSCert without TLSKey")
	}
}

// TestSolveOpt_BuildArgs verifies build-args are forwarded as frontend attrs.
func TestSolveOpt_BuildArgs(t *testing.T) {
	c := newWithSolver(Config{Addr: "tcp://localhost:1234"}, &fakeSolverImpl{resp: &bkclient.SolveResponse{}})
	opt := c.solveOpt(BuildRequest{
		SourceDir:  "/tmp/src",
		PushTarget: "registry/img:tag",
		BuildArgs:  map[string]string{"ENV": "prod"},
	})
	if opt.FrontendAttrs["build-arg:ENV"] != "prod" {
		t.Fatalf("build arg not forwarded: %v", opt.FrontendAttrs)
	}
}

// TestSolveOpt_CacheFrom verifies CacheFrom is passed as a registry cache import.
func TestSolveOpt_CacheFrom(t *testing.T) {
	c := newWithSolver(Config{Addr: "tcp://localhost:1234"}, &fakeSolverImpl{resp: &bkclient.SolveResponse{}})
	opt := c.solveOpt(BuildRequest{
		SourceDir:  "/tmp/src",
		PushTarget: "registry/img:tag",
		CacheFrom:  "registry/cache:latest",
	})
	if len(opt.CacheImports) != 1 || opt.CacheImports[0].Attrs["ref"] != "registry/cache:latest" {
		t.Fatalf("unexpected cache imports: %v", opt.CacheImports)
	}
}

// TestSolveOpt_Platform verifies DefaultPlatform is forwarded.
func TestSolveOpt_Platform(t *testing.T) {
	c := newWithSolver(Config{Addr: "tcp://localhost:1234", DefaultPlatform: "linux/arm64"}, &fakeSolverImpl{resp: &bkclient.SolveResponse{}})
	opt := c.solveOpt(BuildRequest{SourceDir: "/tmp", PushTarget: "r/i:t"})
	if opt.FrontendAttrs["platform"] != "linux/arm64" {
		t.Fatalf("platform not set: %v", opt.FrontendAttrs)
	}
}

// fakeSolverImpl is the concrete fake used in most tests. It feeds log data
// into the statusChan before returning.
type fakeSolverImpl struct {
	resp       *bkclient.SolveResponse
	err        error
	logs       []string
	sideEffect func() // called before returning, e.g. to cancel ctx
}

func (f *fakeSolverImpl) Solve(ctx context.Context, _ *llb.Definition, _ bkclient.SolveOpt, statusChan chan *bkclient.SolveStatus) (*bkclient.SolveResponse, error) {
	if statusChan != nil {
		for _, line := range f.logs {
			statusChan <- &bkclient.SolveStatus{
				Logs: []*bkclient.VertexLog{
					{Data: []byte(line + "\n")},
				},
			}
		}
		close(statusChan)
	}
	if f.sideEffect != nil {
		f.sideEffect()
	}
	return f.resp, f.err
}
