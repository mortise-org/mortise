package git

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	gogitea "code.gitea.io/sdk/gitea"
)

// GiteaAPI implements GitAPI for Gitea / Forgejo via OAuth token.
type GiteaAPI struct {
	client *gogitea.Client
	token  string // OAuth access token (also used for git clone)
	secret string // webhook HMAC secret
}

// NewGiteaAPI constructs a GiteaAPI. baseURL is e.g. "https://gitea.internal.example".
func NewGiteaAPI(baseURL, token, webhookSecret string) (*GiteaAPI, error) {
	// SetGiteaVersion("") skips the server version query on construction,
	// avoiding a network call at startup. Actual API calls will still use
	// the correct server version at call time.
	c, err := gogitea.NewClient(strings.TrimRight(baseURL, "/"),
		gogitea.SetToken(token),
		gogitea.SetGiteaVersion(""))
	if err != nil {
		return nil, fmt.Errorf("new gitea client: %w", err)
	}
	return &GiteaAPI{client: c, token: token, secret: webhookSecret}, nil
}

func (g *GiteaAPI) RegisterWebhook(ctx context.Context, repo string, cfg WebhookConfig) error {
	_, _ = ctx, repo
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	events := cfg.Events
	if len(events) == 0 {
		events = []string{"push", "pull_request"}
	}
	hookCfg := map[string]string{
		"url":          cfg.URL,
		"content_type": "json",
		"secret":       cfg.Secret,
	}
	_, _, err = g.client.CreateRepoHook(owner, name, gogitea.CreateHookOption{
		Type:   gogitea.HookTypeGitea,
		Config: hookCfg,
		Events: events,
		Active: true,
	})
	return err
}

func (g *GiteaAPI) ListWebhooks(ctx context.Context, repo string) ([]WebhookInfo, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	hooks, _, err := g.client.ListRepoHooks(owner, name, gogitea.ListHooksOptions{})
	if err != nil {
		return nil, fmt.Errorf("list gitea hooks: %w", err)
	}
	result := make([]WebhookInfo, 0, len(hooks))
	for _, h := range hooks {
		result = append(result, WebhookInfo{
			ID:     h.ID,
			URL:    h.Config["url"],
			Active: h.Active,
		})
	}
	return result, nil
}

func (g *GiteaAPI) DeleteWebhook(ctx context.Context, repo string, hookID int64) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	_, err = g.client.DeleteRepoHook(owner, name, hookID)
	return err
}

func (g *GiteaAPI) PostCommitStatus(_ context.Context, repo, sha string, status CommitStatus) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	_, _, err = g.client.CreateStatus(owner, name, sha, gogitea.CreateStatusOption{
		State:       giteaState(status.State),
		TargetURL:   status.TargetURL,
		Description: status.Description,
		Context:     status.Context,
	})
	return err
}

func (g *GiteaAPI) VerifyWebhookSignature(body []byte, header http.Header) error {
	// Gitea signs with HMAC-SHA256, delivers in X-Gitea-Signature.
	sig := header.Get("X-Gitea-Signature")
	if sig == "" {
		return fmt.Errorf("missing X-Gitea-Signature header")
	}
	mac := hmac.New(sha256.New, []byte(g.secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

func (g *GiteaAPI) ResolveCloneCredentials(_ context.Context, _ string) (GitCredentials, error) {
	return GitCredentials{Token: g.token}, nil
}

func (g *GiteaAPI) ListRepos(ctx context.Context) ([]Repository, error) {
	repos, _, err := g.client.ListMyRepos(gogitea.ListReposOptions{
		ListOptions: gogitea.ListOptions{PageSize: 100},
	})
	if err != nil {
		return nil, fmt.Errorf("list gitea repos: %w", err)
	}
	result := make([]Repository, 0, len(repos))
	for _, r := range repos {
		updatedAt := ""
		if !r.Updated.IsZero() {
			updatedAt = r.Updated.Format("2006-01-02T15:04:05Z")
		}
		result = append(result, Repository{
			FullName:      r.FullName,
			Name:          r.Name,
			Description:   r.Description,
			DefaultBranch: r.DefaultBranch,
			CloneURL:      r.CloneURL,
			UpdatedAt:     updatedAt,
			Language:      r.Language,
			Private:       r.Private,
		})
	}
	return result, nil
}

func (g *GiteaAPI) ListBranches(ctx context.Context, repo string) ([]Branch, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	branches, _, err := g.client.ListRepoBranches(owner, name, gogitea.ListRepoBranchesOptions{
		ListOptions: gogitea.ListOptions{PageSize: 100},
	})
	if err != nil {
		return nil, fmt.Errorf("list gitea branches: %w", err)
	}
	// Fetch repo to determine default branch.
	r, _, err := g.client.GetRepo(owner, name)
	if err != nil {
		return nil, fmt.Errorf("get gitea repo: %w", err)
	}
	result := make([]Branch, 0, len(branches))
	for _, b := range branches {
		result = append(result, Branch{
			Name:    b.Name,
			Default: b.Name == r.DefaultBranch,
		})
	}
	return result, nil
}

func (g *GiteaAPI) ListTree(ctx context.Context, owner, repo, branch, path string) ([]TreeEntry, error) {
	_ = ctx
	items, _, err := g.client.ListContents(owner, repo, branch, path)
	if err != nil {
		return nil, fmt.Errorf("list gitea tree: %w", err)
	}
	result := make([]TreeEntry, 0, len(items))
	for _, item := range items {
		entryType := "blob"
		if item.Type == "dir" {
			entryType = "tree"
		}
		result = append(result, TreeEntry{
			Name: item.Name,
			Type: entryType,
			Path: item.Path,
		})
	}
	return result, nil
}

func giteaState(s CommitStatusState) gogitea.StatusState {
	switch s {
	case StatusPending:
		return gogitea.StatusPending
	case StatusSuccess:
		return gogitea.StatusSuccess
	case StatusFailure:
		return gogitea.StatusFailure
	default:
		return gogitea.StatusPending
	}
}
