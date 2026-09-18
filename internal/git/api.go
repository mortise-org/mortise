package git

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// ErrAuthFailed indicates the git token was rejected (401/403). The user
// should reconnect their git provider from their profile.
var ErrAuthFailed = errors.New("git authentication failed")

type WebhookConfig struct {
	URL    string
	Secret string
	Events []string
}

type WebhookInfo struct {
	ID     int64
	URL    string
	Active bool
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

// TreeEntry represents a single entry in a repository tree listing.
type TreeEntry struct {
	Name string `json:"name"`
	Type string `json:"type"` // "tree" (directory) or "blob" (file)
	Path string `json:"path"`
}

// PullRequestAuthor carries normalized author metadata for forge PR/MR APIs.
// IsBot is best-effort and may be false when the forge doesn't expose bot
// identity cleanly in the listing response.
type PullRequestAuthor struct {
	Login string `json:"login"`
	Type  string `json:"type,omitempty"`
	IsBot bool   `json:"isBot,omitempty"`
}

// PullRequestSnapshot is the normalized open PR / MR shape used by preview
// synchronization across all supported forges.
type PullRequestSnapshot struct {
	Number int               `json:"number"`
	Branch string            `json:"branch"`
	SHA    string            `json:"sha"`
	Author PullRequestAuthor `json:"author,omitempty"`
}

// GitAPI handles forge-specific REST API calls. One implementation per forge.
type GitAPI interface {
	RegisterWebhook(ctx context.Context, repo string, cfg WebhookConfig) error
	ListWebhooks(ctx context.Context, repo string) ([]WebhookInfo, error)
	DeleteWebhook(ctx context.Context, repo string, hookID int64) error
	PostCommitStatus(ctx context.Context, repo, sha string, status CommitStatus) error
	VerifyWebhookSignature(body []byte, header http.Header) error
	ResolveCloneCredentials(ctx context.Context, repo string) (GitCredentials, error)
	ListRepos(ctx context.Context) ([]Repository, error)
	ListBranches(ctx context.Context, repo string) ([]Branch, error)
	ResolveBranchHead(ctx context.Context, repo, branch string) (string, error)
	ListOpenPullRequests(ctx context.Context, repo string) ([]PullRequestSnapshot, error)
	ListTree(ctx context.Context, owner, repo, branch, path string) ([]TreeEntry, error)
}

// HostTimeout bounds every HTTP call to a git host. The SDK clients ship
// with no timeout, and a hung connection to a host held the App
// controller's single worker for ~3 minutes -- every App on the platform
// stopped reconciling until the kernel gave up on the socket (CAI-173).
const HostTimeout = 30 * time.Second

// NewHostHTTPClient returns the http.Client git-host SDKs are built on.
func NewHostHTTPClient() *http.Client {
	return &http.Client{Timeout: HostTimeout}
}
