package git

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	gogithub "github.com/google/go-github/v66/github"
	"golang.org/x/oauth2"
)

// GitHubAPI implements GitAPI for GitHub via OAuth token.
type GitHubAPI struct {
	client *gogithub.Client
	token  string // OAuth access token (also used for git clone)
	secret string // webhook HMAC secret
}

// NewGitHubAPI constructs a GitHubAPI. baseURL is e.g. "https://github.com" or a GHE URL.
// token is an OAuth access token. webhookSecret is the HMAC secret for payload verification.
func NewGitHubAPI(baseURL, token, webhookSecret string) (*GitHubAPI, error) {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(context.Background(), ts)
	var c *gogithub.Client
	if baseURL == "" || baseURL == "https://github.com" {
		c = gogithub.NewClient(tc)
	} else {
		var err error
		apiBase := strings.TrimRight(baseURL, "/") + "/api/v3/"
		uploadBase := strings.TrimRight(baseURL, "/") + "/api/uploads/"
		c, err = gogithub.NewClient(tc).WithEnterpriseURLs(apiBase, uploadBase)
		if err != nil {
			return nil, fmt.Errorf("new github enterprise client: %w", err)
		}
	}
	return &GitHubAPI{client: c, token: token, secret: webhookSecret}, nil
}

func (g *GitHubAPI) RegisterWebhook(ctx context.Context, repo string, cfg WebhookConfig) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	events := cfg.Events
	if len(events) == 0 {
		events = []string{"push", "pull_request"}
	}
	hook := &gogithub.Hook{
		Events: events,
		Config: &gogithub.HookConfig{
			URL:         gogithub.String(cfg.URL),
			ContentType: gogithub.String("json"),
			Secret:      gogithub.String(cfg.Secret),
			InsecureSSL: gogithub.String("0"),
		},
		Active: gogithub.Bool(true),
	}
	_, _, err = g.client.Repositories.CreateHook(ctx, owner, name, hook)
	return err
}

func (g *GitHubAPI) PostCommitStatus(ctx context.Context, repo, sha string, status CommitStatus) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	s := &gogithub.RepoStatus{
		State:       gogithub.String(string(status.State)),
		TargetURL:   gogithub.String(status.TargetURL),
		Description: gogithub.String(status.Description),
		Context:     gogithub.String(status.Context),
	}
	_, _, err = g.client.Repositories.CreateStatus(ctx, owner, name, sha, s)
	return err
}

func (g *GitHubAPI) VerifyWebhookSignature(body []byte, header http.Header) error {
	sig := header.Get("X-Hub-Signature-256")
	if sig == "" {
		return fmt.Errorf("missing X-Hub-Signature-256 header")
	}
	sig = strings.TrimPrefix(sig, "sha256=")
	mac := hmac.New(sha256.New, []byte(g.secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

func (g *GitHubAPI) ResolveCloneCredentials(_ context.Context, _ string) (GitCredentials, error) {
	return GitCredentials{Token: g.token}, nil
}

func (g *GitHubAPI) ListRepos(ctx context.Context) ([]Repository, error) {
	opts := &gogithub.RepositoryListByAuthenticatedUserOptions{
		Sort:        "pushed",
		Direction:   "desc",
		Affiliation: "owner,collaborator,organization_member",
		ListOptions: gogithub.ListOptions{
			PerPage: 100,
		},
	}
	repos, _, err := g.client.Repositories.ListByAuthenticatedUser(ctx, opts)
	if err != nil {
		return nil, wrapGitHubError(fmt.Errorf("list github repos: %w", err))
	}
	result := make([]Repository, 0, len(repos))
	for _, r := range repos {
		result = append(result, Repository{
			FullName:      r.GetFullName(),
			Name:          r.GetName(),
			Description:   r.GetDescription(),
			DefaultBranch: r.GetDefaultBranch(),
			CloneURL:      r.GetCloneURL(),
			UpdatedAt:     r.GetUpdatedAt().Format("2006-01-02T15:04:05Z"),
			Language:      r.GetLanguage(),
			Private:       r.GetPrivate(),
		})
	}
	return result, nil
}

func (g *GitHubAPI) ListBranches(ctx context.Context, repo string) ([]Branch, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	branches, _, err := g.client.Repositories.ListBranches(ctx, owner, name, &gogithub.BranchListOptions{
		ListOptions: gogithub.ListOptions{PerPage: 100},
	})
	if err != nil {
		return nil, fmt.Errorf("list github branches: %w", err)
	}
	// Fetch default branch name.
	r, _, err := g.client.Repositories.Get(ctx, owner, name)
	if err != nil {
		return nil, fmt.Errorf("get github repo: %w", err)
	}
	defaultBranch := r.GetDefaultBranch()
	result := make([]Branch, 0, len(branches))
	for _, b := range branches {
		result = append(result, Branch{
			Name:    b.GetName(),
			Default: b.GetName() == defaultBranch,
		})
	}
	return result, nil
}

func (g *GitHubAPI) ListTree(ctx context.Context, owner, repo, branch, path string) ([]TreeEntry, error) {
	opts := &gogithub.RepositoryContentGetOptions{Ref: branch}
	_, contents, _, err := g.client.Repositories.GetContents(ctx, owner, repo, path, opts)
	if err != nil {
		return nil, fmt.Errorf("list github tree: %w", err)
	}
	result := make([]TreeEntry, 0, len(contents))
	for _, item := range contents {
		entryType := "blob"
		if item.GetType() == "dir" {
			entryType = "tree"
		}
		result = append(result, TreeEntry{
			Name: item.GetName(),
			Type: entryType,
			Path: item.GetPath(),
		})
	}
	return result, nil
}

// wrapGitHubError checks if a go-github error is a 401/403 and wraps it
// with ErrAuthFailed for downstream detection.
func wrapGitHubError(err error) error {
	if err == nil {
		return nil
	}
	var ghErr *gogithub.ErrorResponse
	if errors.Is(err, ghErr) || errors.As(err, &ghErr) {
		if ghErr.Response != nil && (ghErr.Response.StatusCode == 401 || ghErr.Response.StatusCode == 403) {
			return fmt.Errorf("%w: token may be expired or revoked (HTTP %d)", ErrAuthFailed, ghErr.Response.StatusCode)
		}
	}
	return err
}

// splitRepo splits "owner/repo" into two parts.
func splitRepo(repo string) (string, string, error) {
	// Strip scheme/host if provided as full URL.
	repo = strings.TrimPrefix(repo, "https://")
	repo = strings.TrimPrefix(repo, "http://")
	// Remove host if present (e.g. "github.com/owner/repo" → "owner/repo").
	if idx := strings.Index(repo, "/"); idx != -1 {
		parts := strings.SplitN(repo, "/", 3)
		if len(parts) == 3 {
			// host/owner/repo
			return parts[1], parts[2], nil
		}
		// owner/repo
		return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
	}
	return "", "", fmt.Errorf("repo %q must be in owner/repo format", repo)
}
