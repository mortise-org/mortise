package git

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"time"

	gogitlab "gitlab.com/gitlab-org/api/client-go"
	"golang.org/x/oauth2"
)

// GitLabAPI implements GitAPI for GitLab (.com or self-hosted) via OAuth token.
type GitLabAPI struct {
	client *gogitlab.Client
	token  string // OAuth access token (also used for git clone)
	secret string // webhook token for verification
}

// NewGitLabAPI constructs a GitLabAPI. baseURL is e.g. "https://gitlab.com" or a self-hosted URL.
func NewGitLabAPI(baseURL, token, webhookSecret string) (*GitLabAPI, error) {
	var opts []gogitlab.ClientOptionFunc
	if baseURL != "" && baseURL != "https://gitlab.com" {
		opts = append(opts, gogitlab.WithBaseURL(strings.TrimRight(baseURL, "/")+"/api/v4/"))
	}
	ts := gogitlab.OAuthTokenSource{
		TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}),
	}
	c, err := gogitlab.NewAuthSourceClient(ts, opts...)
	if err != nil {
		return nil, fmt.Errorf("new gitlab client: %w", err)
	}
	return &GitLabAPI{client: c, token: token, secret: webhookSecret}, nil
}

func (g *GitLabAPI) RegisterWebhook(ctx context.Context, repo string, cfg WebhookConfig) error {
	// GitLab uses a numeric project ID or "namespace/repo" path.
	_, _ = ctx, repo
	pushEvents := true
	mergeRequestEvents := true
	token := cfg.Secret
	opts := &gogitlab.AddProjectHookOptions{
		URL:                   gogitlab.Ptr(cfg.URL),
		PushEvents:            gogitlab.Ptr(pushEvents),
		MergeRequestsEvents:   gogitlab.Ptr(mergeRequestEvents),
		Token:                 gogitlab.Ptr(token),
		EnableSSLVerification: gogitlab.Ptr(true),
	}
	_, _, err := g.client.Projects.AddProjectHook(repo, opts)
	return err
}

func (g *GitLabAPI) ListWebhooks(ctx context.Context, repo string) ([]WebhookInfo, error) {
	hooks, _, err := g.client.Projects.ListProjectHooks(repo, nil)
	if err != nil {
		return nil, fmt.Errorf("list gitlab hooks: %w", err)
	}
	result := make([]WebhookInfo, 0, len(hooks))
	for _, h := range hooks {
		active := h.DisabledUntil == nil || h.DisabledUntil.Before(time.Now())
		result = append(result, WebhookInfo{
			ID:     h.ID,
			URL:    h.URL,
			Active: active,
		})
	}
	return result, nil
}

func (g *GitLabAPI) DeleteWebhook(ctx context.Context, repo string, hookID int64) error {
	_, err := g.client.Projects.DeleteProjectHook(repo, hookID)
	return err
}

func (g *GitLabAPI) PostCommitStatus(ctx context.Context, repo, sha string, status CommitStatus) error {
	_, _ = ctx, repo
	state := gogitlab.BuildStateValue(gitlabState(status.State))
	opts := &gogitlab.SetCommitStatusOptions{
		State:       state,
		TargetURL:   gogitlab.Ptr(status.TargetURL),
		Description: gogitlab.Ptr(status.Description),
		Name:        gogitlab.Ptr(status.Context),
	}
	_, _, err := g.client.Commits.SetCommitStatus(repo, sha, opts)
	return err
}

func (g *GitLabAPI) VerifyWebhookSignature(_ []byte, header http.Header) error {
	// GitLab sends the token in X-Gitlab-Token; compare with constant-time equals.
	got := header.Get("X-Gitlab-Token")
	if got == "" {
		return fmt.Errorf("missing X-Gitlab-Token header")
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(g.secret)) != 1 {
		return fmt.Errorf("token mismatch")
	}
	return nil
}

func (g *GitLabAPI) ResolveCloneCredentials(_ context.Context, _ string) (GitCredentials, error) {
	return GitCredentials{Token: g.token}, nil
}

func (g *GitLabAPI) ListRepos(ctx context.Context) ([]Repository, error) {
	membership := true
	orderBy := "last_activity_at"
	sortDir := "desc"
	opts := &gogitlab.ListProjectsOptions{
		Membership: gogitlab.Ptr(membership),
		OrderBy:    gogitlab.Ptr(orderBy),
		Sort:       gogitlab.Ptr(sortDir),
		ListOptions: gogitlab.ListOptions{
			PerPage: 100,
		},
	}
	projects, _, err := g.client.Projects.ListProjects(opts)
	if err != nil {
		return nil, fmt.Errorf("list gitlab projects: %w", err)
	}
	result := make([]Repository, 0, len(projects))
	for _, p := range projects {
		updatedAt := ""
		if p.LastActivityAt != nil {
			updatedAt = p.LastActivityAt.Format("2006-01-02T15:04:05Z")
		}
		// GitLab uses Visibility to indicate public/internal/private.
		private := p.Visibility == gogitlab.PrivateVisibility
		result = append(result, Repository{
			FullName:      p.PathWithNamespace,
			Name:          p.Name,
			Description:   p.Description,
			DefaultBranch: p.DefaultBranch,
			CloneURL:      p.HTTPURLToRepo,
			UpdatedAt:     updatedAt,
			Language:      "",
			Private:       private,
		})
	}
	return result, nil
}

func (g *GitLabAPI) ListBranches(ctx context.Context, repo string) ([]Branch, error) {
	branches, _, err := g.client.Branches.ListBranches(repo, &gogitlab.ListBranchesOptions{
		ListOptions: gogitlab.ListOptions{PerPage: 100},
	})
	if err != nil {
		return nil, fmt.Errorf("list gitlab branches: %w", err)
	}
	result := make([]Branch, 0, len(branches))
	for _, b := range branches {
		result = append(result, Branch{
			Name:    b.Name,
			Default: b.Default,
		})
	}
	return result, nil
}

func (g *GitLabAPI) ListTree(ctx context.Context, owner, repo, branch, path string) ([]TreeEntry, error) {
	_ = ctx
	projectPath := owner + "/" + repo
	opts := &gogitlab.ListTreeOptions{
		Path: gogitlab.Ptr(path),
		Ref:  gogitlab.Ptr(branch),
		ListOptions: gogitlab.ListOptions{
			PerPage: 100,
		},
	}
	nodes, _, err := g.client.Repositories.ListTree(projectPath, opts)
	if err != nil {
		return nil, fmt.Errorf("list gitlab tree: %w", err)
	}
	result := make([]TreeEntry, 0, len(nodes))
	for _, n := range nodes {
		entryType := "blob"
		if n.Type == "tree" {
			entryType = "tree"
		}
		result = append(result, TreeEntry{
			Name: n.Name,
			Type: entryType,
			Path: n.Path,
		})
	}
	return result, nil
}

// gitlabState maps Mortise's CommitStatusState to GitLab build state strings.
func gitlabState(s CommitStatusState) string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusSuccess:
		return "success"
	case StatusFailure:
		return "failed"
	default:
		return "pending"
	}
}
