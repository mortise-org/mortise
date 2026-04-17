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

// Repository represents a git repository returned by the forge API.
type Repository struct {
	FullName      string `json:"fullName"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	DefaultBranch string `json:"defaultBranch"`
	CloneURL      string `json:"cloneURL"`
	UpdatedAt     string `json:"updatedAt"`
	Language      string `json:"language"`
	Private       bool   `json:"private"`
}

// Branch represents a git branch within a repository.
type Branch struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

// GitAPI handles forge-specific REST API calls. One implementation per forge.
type GitAPI interface {
	RegisterWebhook(ctx context.Context, repo string, cfg WebhookConfig) error
	PostCommitStatus(ctx context.Context, repo, sha string, status CommitStatus) error
	VerifyWebhookSignature(body []byte, header http.Header) error
	ResolveCloneCredentials(ctx context.Context, repo string) (GitCredentials, error)
	ListRepos(ctx context.Context) ([]Repository, error)
	ListBranches(ctx context.Context, repo string) ([]Branch, error)
}
