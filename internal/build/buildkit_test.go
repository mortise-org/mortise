package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	exptypes "github.com/moby/buildkit/exporter/containerimage/exptypes"
)

// testSourceDir creates a temp directory with a minimal Dockerfile so tests
// use the Dockerfile path (not Railpack).
func testSourceDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

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

	srcDir := testSourceDir(t)
	ch, err := c.Submit(context.Background(), BuildRequest{
		SourceDir:  srcDir,
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

	srcDir := testSourceDir(t)
	ch, err := c.Submit(context.Background(), BuildRequest{
		SourceDir:  srcDir,
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

	srcDir := testSourceDir(t)
	ch, err := c.Submit(ctx, BuildRequest{
		SourceDir:  srcDir,
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

// TestNew_EmptyAddr verifies that New with an empty address does not panic.
// The upstream BuildKit client defers connection errors to Solve time.
func TestNew_EmptyAddr(t *testing.T) {
	// Should not panic; connection error surfaces later.
	_, _ = New(Config{})
}

// TestSolveOpt_BuildArgs verifies build-args are forwarded as frontend attrs.
func TestSolveOpt_BuildArgs(t *testing.T) {
	c := newWithSolver(Config{Addr: "tcp://localhost:1234"}, &fakeSolverImpl{resp: &bkclient.SolveResponse{}})
	opt := c.dockerfileSolveOpt(BuildRequest{
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
	opt := c.dockerfileSolveOpt(BuildRequest{
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
	opt := c.dockerfileSolveOpt(BuildRequest{SourceDir: "/tmp", PushTarget: "r/i:t"})
	if opt.FrontendAttrs["platform"] != "linux/arm64" {
		t.Fatalf("platform not set: %v", opt.FrontendAttrs)
	}
}

// makeMonorepoTree creates a repo-root / subdir pair with a Dockerfile at a
// location determined by the caller. Returns (rootDir, subDir).
func makeMonorepoTree(t *testing.T, subPath string, rootDockerfile, subDockerfile string) (string, string) {
	t.Helper()
	root := t.TempDir()
	sub := filepath.Join(root, subPath)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if rootDockerfile != "" {
		if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte(rootDockerfile), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if subDockerfile != "" {
		if err := os.WriteFile(filepath.Join(sub, "Dockerfile"), []byte(subDockerfile), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, sub
}

// TestResolveContext_AutoSelfContained: Dockerfile in subdir with no
// repo-root-prefixed COPY → context collapses to subdir.
func TestResolveContext_AutoSelfContained(t *testing.T) {
	root, sub := makeMonorepoTree(t, "services/api", "", "FROM alpine\nCOPY . .\n")
	c := newWithSolver(Config{}, &fakeSolverImpl{})
	req := BuildRequest{SourceDir: root, DockerfileDir: sub, PushTarget: "r/i:t"}
	ch := make(chan BuildEvent, 8)

	ok := c.resolveDockerfileContext(&req, "Dockerfile", ch)
	if !ok {
		t.Fatal("expected Dockerfile path to be taken")
	}
	if req.SourceDir != sub {
		t.Fatalf("SourceDir = %q, want %q (self-contained)", req.SourceDir, sub)
	}
}

// TestResolveContext_AutoHeuristicFallback: Dockerfile in subdir whose COPY
// references the subdir prefix → context falls back to repo root.
func TestResolveContext_AutoHeuristicFallback(t *testing.T) {
	df := "FROM alpine\nCOPY services/api/entrypoint.sh /entrypoint.sh\n"
	root, sub := makeMonorepoTree(t, "services/api", "", df)
	c := newWithSolver(Config{}, &fakeSolverImpl{})
	req := BuildRequest{SourceDir: root, DockerfileDir: sub, PushTarget: "r/i:t"}
	ch := make(chan BuildEvent, 8)

	ok := c.resolveDockerfileContext(&req, "Dockerfile", ch)
	if !ok {
		t.Fatal("expected Dockerfile path to be taken")
	}
	if req.SourceDir != root {
		t.Fatalf("SourceDir = %q, want repo root %q", req.SourceDir, root)
	}
	if req.DockerfileDir != sub {
		t.Fatalf("DockerfileDir = %q, want %q", req.DockerfileDir, sub)
	}
}

// TestResolveContext_AutoHeuristicIgnoresStageCopy: `COPY --from=<stage>`
// copies read from a build stage, not the local context — their sources
// must not trigger the root fallback.
func TestResolveContext_AutoHeuristicIgnoresStageCopy(t *testing.T) {
	df := "FROM alpine AS builder\nFROM alpine\nCOPY --from=builder services/api/bin /bin\n"
	root, sub := makeMonorepoTree(t, "services/api", "", df)
	c := newWithSolver(Config{}, &fakeSolverImpl{})
	req := BuildRequest{SourceDir: root, DockerfileDir: sub, PushTarget: "r/i:t"}
	ch := make(chan BuildEvent, 8)

	ok := c.resolveDockerfileContext(&req, "Dockerfile", ch)
	if !ok {
		t.Fatal("expected Dockerfile path to be taken")
	}
	if req.SourceDir != sub {
		t.Fatalf("SourceDir = %q, want subdir %q (stage copies should not trigger fallback)", req.SourceDir, sub)
	}
}

// TestResolveContext_ExplicitRoot: context override forces repo root even
// when a self-contained Dockerfile exists in the subdir.
func TestResolveContext_ExplicitRoot(t *testing.T) {
	root, sub := makeMonorepoTree(t, "services/api", "", "FROM alpine\nCOPY . .\n")
	c := newWithSolver(Config{}, &fakeSolverImpl{})
	req := BuildRequest{SourceDir: root, DockerfileDir: sub, ContextMode: ContextModeRoot, PushTarget: "r/i:t"}
	ch := make(chan BuildEvent, 8)

	ok := c.resolveDockerfileContext(&req, "Dockerfile", ch)
	if !ok {
		t.Fatal("expected Dockerfile path to be taken")
	}
	if req.SourceDir != root {
		t.Fatalf("SourceDir = %q, want repo root %q", req.SourceDir, root)
	}
	if req.DockerfileDir != sub {
		t.Fatalf("DockerfileDir = %q, want %q", req.DockerfileDir, sub)
	}
}

// TestResolveContext_ExplicitSubdir: context override forces subdir even
// when the Dockerfile has repo-root-prefixed COPY (heuristic would otherwise
// pick root).
func TestResolveContext_ExplicitSubdir(t *testing.T) {
	df := "FROM alpine\nCOPY services/api/entrypoint.sh /entrypoint.sh\n"
	root, sub := makeMonorepoTree(t, "services/api", "", df)
	c := newWithSolver(Config{}, &fakeSolverImpl{})
	req := BuildRequest{SourceDir: root, DockerfileDir: sub, ContextMode: ContextModeSubdir, PushTarget: "r/i:t"}
	ch := make(chan BuildEvent, 8)

	ok := c.resolveDockerfileContext(&req, "Dockerfile", ch)
	if !ok {
		t.Fatal("expected Dockerfile path to be taken")
	}
	if req.SourceDir != sub {
		t.Fatalf("SourceDir = %q, want %q", req.SourceDir, sub)
	}
}

// TestResolveContext_MonorepoRoot: no subdir Dockerfile, repo-root Dockerfile
// exists → keep repo-root context, point DockerfileDir there.
func TestResolveContext_MonorepoRoot(t *testing.T) {
	root, sub := makeMonorepoTree(t, "services/api", "FROM alpine\n", "")
	c := newWithSolver(Config{}, &fakeSolverImpl{})
	req := BuildRequest{SourceDir: root, DockerfileDir: sub, PushTarget: "r/i:t"}
	ch := make(chan BuildEvent, 8)

	ok := c.resolveDockerfileContext(&req, "Dockerfile", ch)
	if !ok {
		t.Fatal("expected Dockerfile path to be taken")
	}
	if req.SourceDir != root {
		t.Fatalf("SourceDir = %q, want %q", req.SourceDir, root)
	}
	if req.DockerfileDir != root {
		t.Fatalf("DockerfileDir = %q, want %q", req.DockerfileDir, root)
	}
}

// TestDockerfileNeedsRootContext covers the parser edge cases: flags,
// comments, wildcards, multi-source COPY.
func TestDockerfileNeedsRootContext(t *testing.T) {
	prefix := "services/api/"
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"plain", "FROM alpine\nCOPY services/api/entrypoint.sh /e\n", true},
		{"leading-dot-slash", "FROM alpine\nCOPY ./services/api/entrypoint.sh /e\n", true},
		{"chown-flag", "FROM alpine\nCOPY --chown=1000:1000 services/api/file /f\n", true},
		{"multi-source", "FROM alpine\nCOPY services/api/a services/api/b /dst/\n", true},
		{"stage-copy", "FROM alpine AS b\nFROM alpine\nCOPY --from=b services/api/x /x\n", false},
		{"comment", "FROM alpine\n# COPY services/api/x /x\nCOPY . .\n", false},
		{"add-directive", "FROM alpine\nADD services/api/tarball.tgz /app\n", true},
		{"self-contained", "FROM alpine\nCOPY . .\n", false},
		{"other-subdir", "FROM alpine\nCOPY other/file /x\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "Dockerfile")
			if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			got := dockerfileNeedsRootContext(p, prefix)
			if got != tc.want {
				t.Fatalf("got %v, want %v (body: %q)", got, tc.want, tc.body)
			}
		})
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
