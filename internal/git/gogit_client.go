package git

import (
	"context"
	"fmt"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
)

// GoGitClient implements GitClient using go-git. It is a single implementation
// shared across all forges (git protocol is forge-agnostic).
type GoGitClient struct{}

var _ GitClient = (*GoGitClient)(nil)

// NewGoGitClient returns a GoGitClient.
func NewGoGitClient() *GoGitClient {
	return &GoGitClient{}
}

func (c *GoGitClient) Clone(ctx context.Context, repo, ref, dest string, creds GitCredentials) error {
	_ = ctx
	opts := &gogit.CloneOptions{
		URL:           repo,
		ReferenceName: plumbing.NewBranchReferenceName(ref),
		SingleBranch:  true,
		Depth:         1,
	}
	if creds.Token != "" {
		opts.Auth = &http.BasicAuth{
			Username: "x-token", // GitHub/GitLab/Gitea all accept any username with a token
			Password: creds.Token,
		}
	}
	_, err := gogit.PlainClone(dest, false, opts)
	if err != nil {
		return fmt.Errorf("clone %s@%s: %w", repo, ref, err)
	}
	return nil
}

func (c *GoGitClient) Fetch(ctx context.Context, dir, ref string) error {
	_ = ctx
	r, err := gogit.PlainOpen(dir)
	if err != nil {
		return fmt.Errorf("open repo %s: %w", dir, err)
	}
	wt, err := r.Worktree()
	if err != nil {
		return fmt.Errorf("worktree: %w", err)
	}
	err = wt.Pull(&gogit.PullOptions{
		ReferenceName: plumbing.NewBranchReferenceName(ref),
		SingleBranch:  true,
	})
	if err != nil && err != gogit.NoErrAlreadyUpToDate {
		return fmt.Errorf("fetch %s: %w", ref, err)
	}
	return nil
}
