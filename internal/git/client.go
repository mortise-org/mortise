package git

import "context"

// GitClient handles git protocol operations. Single implementation shared across all forges.
type GitClient interface {
	Clone(ctx context.Context, repo, ref, dest string, creds GitCredentials) error
	CheckoutRevision(ctx context.Context, dir, revision string) error
	Fetch(ctx context.Context, dir, ref string) error
}
