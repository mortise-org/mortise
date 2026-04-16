package git

import "context"

// GitClient handles git protocol operations. Single implementation shared across all forges.
type GitClient interface {
	Clone(ctx context.Context, repo, ref, dest string, creds GitCredentials) error
	Fetch(ctx context.Context, dir, ref string) error
}
