package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestGoGitClientCheckoutRevisionAfterClone(t *testing.T) {
	sourceDir, branch, firstRevision := createTestBranchRepo(t)
	cloneDir := t.TempDir()

	client := NewGoGitClient()
	if err := client.Clone(context.Background(), sourceDir, branch, cloneDir, GitCredentials{}); err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	if err := client.CheckoutRevision(context.Background(), cloneDir, firstRevision); err != nil {
		t.Fatalf("CheckoutRevision() error = %v", err)
	}

	got, err := os.ReadFile(filepath.Join(cloneDir, "version.txt"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != "old\n" {
		t.Fatalf("version.txt = %q, want %q", string(got), "old\n")
	}
}

func createTestBranchRepo(t *testing.T) (repoDir, branch, firstRevision string) {
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
	writeCommit(t, wt, repoDir, "seed.txt", "seed\n", "seed")

	branch = "feature/preview-slash"
	if err := wt.Checkout(&gogit.CheckoutOptions{
		Create: true,
		Branch: plumbing.NewBranchReferenceName(branch),
	}); err != nil {
		t.Fatalf("Checkout(create branch) error = %v", err)
	}

	firstRevision = writeCommit(t, wt, repoDir, "version.txt", "old\n", "first")
	writeCommit(t, wt, repoDir, "version.txt", "new\n", "second")
	return repoDir, branch, firstRevision
}

func writeCommit(t *testing.T, wt *gogit.Worktree, repoDir, path, contents, message string) string {
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
