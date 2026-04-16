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
