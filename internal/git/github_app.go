package git

import (
	"context"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	gogithub "github.com/google/go-github/v66/github"
)

// GitHubAppAPI implements GitAPI for GitHub using App-based authentication.
// It generates JWTs from the app's private key, then obtains scoped
// installation tokens for API calls.
type GitHubAppAPI struct {
	appID      int64
	privateKey *rsa.PrivateKey
	secret     string // webhook HMAC secret
	baseURL    string // empty for github.com

	mu             sync.Mutex
	cachedToken    string
	cachedTokenExp time.Time
	installationID int64
}

// NewGitHubAppAPI constructs a GitHubAppAPI from a private key PEM, app ID,
// and webhook secret. baseURL is e.g. "https://github.example.com" or empty
// for github.com.
func NewGitHubAppAPI(baseURL string, appID int64, privateKeyPEM []byte, webhookSecret string) (*GitHubAppAPI, error) {
	block, _ := pem.Decode(privateKeyPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block from private key")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		// Try PKCS8 as fallback.
		k, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err2 != nil {
			return nil, fmt.Errorf("parse private key: %w (pkcs8: %w)", err, err2)
		}
		var ok bool
		key, ok = k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("private key is not RSA")
		}
	}
	return &GitHubAppAPI{
		appID:      appID,
		privateKey: key,
		secret:     webhookSecret,
		baseURL:    baseURL,
	}, nil
}

// SetInstallationID sets the installation to use for API calls.
func (g *GitHubAppAPI) SetInstallationID(id int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.installationID = id
	// Invalidate cached token when installation changes.
	g.cachedToken = ""
}

// generateJWT creates a short-lived JWT signed with the app's private key.
func (g *GitHubAppAPI) generateJWT() (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now.Add(-60 * time.Second)),
		ExpiresAt: jwt.NewNumericDate(now.Add(10 * time.Minute)),
		Issuer:    fmt.Sprintf("%d", g.appID),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(g.privateKey)
}

// appClient returns a go-github client authenticated as the App (JWT).
func (g *GitHubAppAPI) appClient() (*gogithub.Client, error) {
	jwtToken, err := g.generateJWT()
	if err != nil {
		return nil, fmt.Errorf("generate app jwt: %w", err)
	}
	transport := &jwtTransport{token: jwtToken, base: http.DefaultTransport}
	httpClient := &http.Client{Transport: transport}
	c := gogithub.NewClient(httpClient)
	if g.baseURL != "" && g.baseURL != "https://github.com" {
		apiBase := strings.TrimRight(g.baseURL, "/") + "/api/v3/"
		uploadBase := strings.TrimRight(g.baseURL, "/") + "/api/uploads/"
		c, err = c.WithEnterpriseURLs(apiBase, uploadBase)
		if err != nil {
			return nil, fmt.Errorf("github enterprise urls: %w", err)
		}
	}
	return c, nil
}

// installationToken returns a cached or fresh installation access token.
func (g *GitHubAppAPI) installationToken(ctx context.Context) (string, error) {
	g.mu.Lock()
	instID := g.installationID
	if g.cachedToken != "" && time.Now().Before(g.cachedTokenExp) {
		tok := g.cachedToken
		g.mu.Unlock()
		return tok, nil
	}
	g.mu.Unlock()

	if instID == 0 {
		// Auto-discover the first installation.
		c, err := g.appClient()
		if err != nil {
			return "", err
		}
		installations, _, err := c.Apps.ListInstallations(ctx, nil)
		if err != nil {
			return "", fmt.Errorf("list installations: %w", err)
		}
		if len(installations) == 0 {
			return "", fmt.Errorf("no installations found for GitHub App %d", g.appID)
		}
		instID = installations[0].GetID()
		g.mu.Lock()
		g.installationID = instID
		g.mu.Unlock()
	}

	c, err := g.appClient()
	if err != nil {
		return "", err
	}
	token, _, err := c.Apps.CreateInstallationToken(ctx, instID, nil)
	if err != nil {
		return "", fmt.Errorf("create installation token: %w", err)
	}

	g.mu.Lock()
	g.cachedToken = token.GetToken()
	g.cachedTokenExp = token.GetExpiresAt().Time.Add(-5 * time.Minute)
	g.mu.Unlock()

	return token.GetToken(), nil
}

// installationClient returns a go-github client authenticated with an
// installation token.
func (g *GitHubAppAPI) installationClient(ctx context.Context) (*gogithub.Client, error) {
	tok, err := g.installationToken(ctx)
	if err != nil {
		return nil, err
	}
	transport := &jwtTransport{token: tok, base: http.DefaultTransport}
	httpClient := &http.Client{Transport: transport}
	c := gogithub.NewClient(httpClient)
	if g.baseURL != "" && g.baseURL != "https://github.com" {
		apiBase := strings.TrimRight(g.baseURL, "/") + "/api/v3/"
		uploadBase := strings.TrimRight(g.baseURL, "/") + "/api/uploads/"
		c, err = c.WithEnterpriseURLs(apiBase, uploadBase)
		if err != nil {
			return nil, fmt.Errorf("github enterprise urls: %w", err)
		}
	}
	return c, nil
}

func (g *GitHubAppAPI) RegisterWebhook(ctx context.Context, repo string, cfg WebhookConfig) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	c, err := g.installationClient(ctx)
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
	_, _, err = c.Repositories.CreateHook(ctx, owner, name, hook)
	return err
}

func (g *GitHubAppAPI) PostCommitStatus(ctx context.Context, repo, sha string, status CommitStatus) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	c, err := g.installationClient(ctx)
	if err != nil {
		return err
	}
	s := &gogithub.RepoStatus{
		State:       gogithub.String(string(status.State)),
		TargetURL:   gogithub.String(status.TargetURL),
		Description: gogithub.String(status.Description),
		Context:     gogithub.String(status.Context),
	}
	_, _, err = c.Repositories.CreateStatus(ctx, owner, name, sha, s)
	return err
}

func (g *GitHubAppAPI) VerifyWebhookSignature(body []byte, header http.Header) error {
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

func (g *GitHubAppAPI) ResolveCloneCredentials(ctx context.Context, _ string) (GitCredentials, error) {
	tok, err := g.installationToken(ctx)
	if err != nil {
		return GitCredentials{}, err
	}
	return GitCredentials{Token: tok}, nil
}

func (g *GitHubAppAPI) ListRepos(ctx context.Context) ([]Repository, error) {
	c, err := g.installationClient(ctx)
	if err != nil {
		return nil, err
	}
	opts := &gogithub.ListOptions{PerPage: 100}
	repos, _, err := c.Apps.ListRepos(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("list installation repos: %w", err)
	}
	result := make([]Repository, 0, len(repos.Repositories))
	for _, r := range repos.Repositories {
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

func (g *GitHubAppAPI) ListBranches(ctx context.Context, repo string) ([]Branch, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	c, err := g.installationClient(ctx)
	if err != nil {
		return nil, err
	}
	branches, _, err := c.Repositories.ListBranches(ctx, owner, name, &gogithub.BranchListOptions{
		ListOptions: gogithub.ListOptions{PerPage: 100},
	})
	if err != nil {
		return nil, fmt.Errorf("list github branches: %w", err)
	}
	r, _, err := c.Repositories.Get(ctx, owner, name)
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

func (g *GitHubAppAPI) ListTree(ctx context.Context, owner, repo, branch, path string) ([]TreeEntry, error) {
	c, err := g.installationClient(ctx)
	if err != nil {
		return nil, err
	}
	opts := &gogithub.RepositoryContentGetOptions{Ref: branch}
	_, contents, _, err := c.Repositories.GetContents(ctx, owner, repo, path, opts)
	if err != nil {
		return nil, fmt.Errorf("list github app tree: %w", err)
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

// jwtTransport adds a Bearer token to every request.
type jwtTransport struct {
	token string
	base  http.RoundTripper
}

func (t *jwtTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := req.Clone(req.Context())
	req2.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req2)
}
