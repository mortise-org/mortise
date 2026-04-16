package git

import (
	"context"
	"net/http"
)

type WebhookConfig struct {
	URL    string
	Secret string
	Events []string
}

type CommitStatusState string

const (
	StatusPending CommitStatusState = "pending"
	StatusSuccess CommitStatusState = "success"
	StatusFailure CommitStatusState = "failure"
)

type CommitStatus struct {
	State       CommitStatusState
	TargetURL   string
	Description string
	Context     string
}

type GitCredentials struct {
	Token string
}

// GitAPI handles forge-specific REST API calls. One implementation per forge.
type GitAPI interface {
	RegisterWebhook(ctx context.Context, repo string, cfg WebhookConfig) error
	PostCommitStatus(ctx context.Context, repo, sha string, status CommitStatus) error
	VerifyWebhookSignature(body []byte, header http.Header) error
	ResolveCloneCredentials(ctx context.Context, repo string) (GitCredentials, error)
}
