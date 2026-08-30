package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/build"
	"github.com/mortise-org/mortise/internal/git"
	"github.com/mortise-org/mortise/internal/registry"
)

func TestRunBuildChecksOutRequestedRevision(t *testing.T) {
	repoDir, branch, firstRevision := createBuildRunnerRepo(t)
	buildClient := &capturingBuildClient{t: t}
	tracker := &buildTracker{revision: firstRevision, phase: buildPhaseRunning}
	ctx, cancel := context.WithCancel(context.Background())

	runBuild(ctx, cancel, tracker, buildParams{
		appName:   "demo",
		namespace: "default",
		repo:      repoDir,
		branch:    branch,
		revision:  firstRevision,
		imageRef: registry.ImageRef{
			Full: "registry.example.com/demo:build",
		},
		pullImageRef: registry.ImageRef{
			Full:     "registry.example.com/demo:build",
			Registry: "registry.example.com",
			Path:     "demo",
		},
		buildContext: mortisev1alpha1.BuildContextRoot,
	}, git.NewGoGitClient(), buildClient, buildRunnerOptions{})

	phase, _, _, _, errMsg, _ := tracker.snapshot()
	if phase != buildPhaseSucceeded {
		t.Fatalf("build phase = %s, err = %q", phase, errMsg)
	}
	if buildClient.version != "old\n" {
		t.Fatalf("built version = %q, want %q", buildClient.version, "old\n")
	}
}

type capturingBuildClient struct {
	t       *testing.T
	version string
}

func (c *capturingBuildClient) Submit(_ context.Context, req build.BuildRequest) (<-chan build.BuildEvent, error) {
	c.t.Helper()
	contents, err := os.ReadFile(filepath.Join(req.SourceDir, "version.txt"))
	if err != nil {
		c.t.Fatalf("ReadFile(version.txt) error = %v", err)
	}
	c.version = string(contents)

	ch := make(chan build.BuildEvent, 1)
	ch <- build.BuildEvent{Type: build.EventSuccess, Digest: "sha256:test"}
	close(ch)
	return ch, nil
}

func createBuildRunnerRepo(t *testing.T) (repoDir, branch, firstRevision string) {
	t.Helper()

	repoDir = t.TempDir()
	repo, err := gogit.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	createBuildRunnerCommit(t, wt, repoDir, "seed.txt", "seed\n", "seed")

	branch = "feature/preview-slash"
	if err := wt.Checkout(&gogit.CheckoutOptions{
		Create: true,
		Branch: plumbing.NewBranchReferenceName(branch),
	}); err != nil {
		t.Fatalf("Checkout(create branch) error = %v", err)
	}

	firstRevision = createBuildRunnerCommit(t, wt, repoDir, "version.txt", "old\n", "first")
	createBuildRunnerCommit(t, wt, repoDir, "version.txt", "new\n", "second")
	return repoDir, branch, firstRevision
}

func createBuildRunnerCommit(t *testing.T, wt *gogit.Worktree, repoDir, path, contents, message string) string {
	t.Helper()

	fullPath := filepath.Join(repoDir, path)
	if err := os.WriteFile(fullPath, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
	if _, err := wt.Add(path); err != nil {
		t.Fatalf("Add(%s) error = %v", path, err)
	}
	hash, err := wt.Commit(message, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "test",
			Email: "test@example.com",
			When:  time.Unix(1, 0),
		},
	})
	if err != nil {
		t.Fatalf("Commit(%s) error = %v", message, err)
	}
	return hash.String()
}

func TestWithRevisionBuildArg(t *testing.T) {
	t.Run("adds the revision beside user args", func(t *testing.T) {
		got := withRevisionBuildArg(map[string]string{"FOO": "bar"}, "main", "abc123")
		if got["MORTISE_REVISION"] != "abc123" || got["FOO"] != "bar" {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("does not mutate the caller's map", func(t *testing.T) {
		in := map[string]string{"FOO": "bar"}
		_ = withRevisionBuildArg(in, "main", "abc123")
		if _, ok := in["MORTISE_REVISION"]; ok {
			t.Fatal("input map was mutated")
		}
	})
	t.Run("user value wins", func(t *testing.T) {
		got := withRevisionBuildArg(map[string]string{"MORTISE_REVISION": "mine"}, "main", "abc123")
		if got["MORTISE_REVISION"] != "mine" {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("absent when the revision is only the branch fallback", func(t *testing.T) {
		if got := withRevisionBuildArg(nil, "main", "main"); got != nil {
			t.Fatalf("got %v", got)
		}
		if got := withRevisionBuildArg(nil, "main", ""); got != nil {
			t.Fatalf("got %v", got)
		}
	})
}
