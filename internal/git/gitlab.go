package git

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"

	gogitlab "github.com/xanzy/go-gitlab"
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
	c, err := gogitlab.NewOAuthClient(token, opts...)
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
