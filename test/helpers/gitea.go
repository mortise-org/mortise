package helpers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// GiteaBootstrap is a bounded test-only helper that provisions content in an
// in-cluster Gitea for integration tests. It speaks only Gitea's public REST
// API; no forge-specific concepts leak outside this file.
//
// Callers give it a base URL (typically a localhost:PORT that a kubectl
// port-forward is tunneling to the in-cluster Gitea Service) along with admin
// credentials matching the user created by test/integration/manifests/20-gitea.yaml.
type GiteaBootstrap struct {
	BaseURL  string // e.g. "http://127.0.0.1:43210"
	Username string // admin user, created by the Gitea deployment postStart hook
	Password string // admin password
}

// BootstrappedRepo identifies a freshly-provisioned Gitea repo along with a
// working API token. The caller feeds Token into a GitProvider token secret
// and CloneURL into App.spec.source.repo.
type BootstrappedRepo struct {
	Owner    string
	Name     string
	CloneURL string // HTTP URL the operator clones from (in-cluster DNS)
	Token    string // personal access token with repo write scope
}

// Ensure creates a repo and uploads the supplied files at HEAD of the default
// branch, then mints an access token for the admin user. All operations are
// idempotent enough for a freshly-created cluster; tests that need isolation
// should use a unique repo name per test.
//
// inClusterBaseURL is the URL the operator (running inside the cluster) will
// use to clone the repo — callers pass the cluster-DNS URL, e.g.
// "http://gitea.mortise-test-deps.svc:3000".
func (g *GiteaBootstrap) Ensure(t *testing.T, inClusterBaseURL, owner, repo string, files map[string]string) *BootstrappedRepo {
	t.Helper()

	// Wait for Gitea's API to be reachable through the port-forward. The
	// postStart hook is async, so we may arrive before the admin user exists.
	g.waitReady(t, 60*time.Second)

	// Token name derives from the test name + the repo name so reruns of the
	// same test create a fresh token (Gitea rejects duplicates) without
	// pulling wall-clock time into tests. Gitea's token-name grammar rejects
	// slashes (subtests embed them) so we sanitize.
	tokenName := sanitizeTokenName("mortise-int-" + t.Name() + "-" + repo)
	token := g.ensureToken(t, tokenName)

	g.ensureRepo(t, owner, repo)

	for path, content := range files {
		g.putFile(t, token, owner, repo, path, content)
	}

	return &BootstrappedRepo{
		Owner:    owner,
		Name:     repo,
		CloneURL: fmt.Sprintf("%s/%s/%s.git", inClusterBaseURL, owner, repo),
		Token:    token,
	}
}

// waitReady polls /api/v1/version until it succeeds or timeout elapses.
func (g *GiteaBootstrap) waitReady(t *testing.T, timeout time.Duration) {
	t.Helper()
	RequireEventually(t, timeout, func() bool {
		req, err := http.NewRequest(http.MethodGet, g.BaseURL+"/api/v1/version", nil)
		if err != nil {
			return false
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	// Also wait until basic auth with the admin user succeeds — the postStart
	// hook runs asynchronously after Gitea is listening.
	RequireEventually(t, timeout, func() bool {
		req, _ := http.NewRequest(http.MethodGet, g.BaseURL+"/api/v1/user", nil)
		req.SetBasicAuth(g.Username, g.Password)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
}

// ensureToken mints a personal access token for the admin user with
// repo-write scope. Each call creates a new token (Gitea rejects duplicate
// names), which is fine for ephemeral test runs.
func (g *GiteaBootstrap) ensureToken(t *testing.T, name string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"name": name,
		"scopes": []string{
			"write:repository",
			"write:user",
			"write:admin",
		},
	})
	req, _ := http.NewRequest(http.MethodPost,
		g.BaseURL+"/api/v1/users/"+g.Username+"/tokens", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(g.Username, g.Password)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("gitea: create token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("gitea: create token status %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Sha1 string `json:"sha1"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("gitea: decode token response: %v", err)
	}
	if out.Sha1 == "" {
		t.Fatal("gitea: empty token returned")
	}
	return out.Sha1
}

// ensureRepo creates the repo under the admin user if it doesn't already exist.
func (g *GiteaBootstrap) ensureRepo(t *testing.T, owner, repo string) {
	t.Helper()
	// HEAD would be prettier, but the public API is GET.
	if g.repoExists(t, owner, repo) {
		return
	}
	// auto_init: false so our uploads below own every commit on the default
	// branch. With auto_init=true Gitea creates a README.md itself, then the
	// caller's putFile() hits "file already exists" unless it first fetches
	// the blob SHA — and the fetch path has its own auth/eventually-consistent
	// quirks we don't need to model.
	body, _ := json.Marshal(map[string]any{
		"name":           repo,
		"auto_init":      false,
		"default_branch": "main",
	})
	req, _ := http.NewRequest(http.MethodPost, g.BaseURL+"/api/v1/user/repos", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(g.Username, g.Password)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("gitea: create repo %s/%s: %v", owner, repo, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusConflict {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("gitea: create repo %s/%s status %d: %s", owner, repo, resp.StatusCode, string(b))
	}
}

func (g *GiteaBootstrap) repoExists(t *testing.T, owner, repo string) bool {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, g.BaseURL+"/api/v1/repos/"+owner+"/"+repo, nil)
	req.SetBasicAuth(g.Username, g.Password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// putFile uploads (or updates) a single file at path on the main branch.
// Uses the "create or update file" API — on conflict we fetch the existing
// SHA and retry as an update. Keeps tests independent of prior state.
func (g *GiteaBootstrap) putFile(t *testing.T, token, owner, repo, path, content string) {
	t.Helper()

	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	payload := map[string]any{
		"message": "integration test: " + path,
		"content": encoded,
		"branch":  "main",
	}

	// If file already exists, pass its SHA so the API treats this as an update.
	if sha := g.fileSHA(token, owner, repo, path); sha != "" {
		payload["sha"] = sha
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/api/v1/repos/%s/%s/contents/%s", g.BaseURL, owner, repo, path),
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("gitea: put file %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("gitea: put file %s status %d: %s", path, resp.StatusCode, string(b))
	}
}

// sanitizeTokenName reduces an arbitrary string to [A-Za-z0-9-_]. Gitea
// rejects characters like "/" (common in Go subtest names) and "%".
func sanitizeTokenName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// fileSHA returns the blob SHA of an existing file, or "" if the file doesn't exist.
func (g *GiteaBootstrap) fileSHA(token, owner, repo, path string) string {
	req, _ := http.NewRequest(http.MethodGet,
		fmt.Sprintf("%s/api/v1/repos/%s/%s/contents/%s", g.BaseURL, owner, repo, path), nil)
	req.Header.Set("Authorization", "token "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var out struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ""
	}
	return out.SHA
}

// GiteaOAuthApp describes a freshly-created OAuth2 application on Gitea.
// The ID is what the admin API accepts for deletion.
type GiteaOAuthApp struct {
	ID           int64
	ClientID     string
	ClientSecret string
}

// CreateOAuthApp creates a new OAuth2 application on Gitea under the admin
// user using the admin credentials stored on the bootstrap. Gitea's API
// returns the plaintext client_secret only in this response, so callers
// must capture it here.
func (g *GiteaBootstrap) CreateOAuthApp(t *testing.T, name string, redirectURIs []string) *GiteaOAuthApp {
	t.Helper()

	body, _ := json.Marshal(map[string]any{
		"name":                name,
		"redirect_uris":       redirectURIs,
		"confidential_client": true,
	})
	req, _ := http.NewRequest(http.MethodPost,
		g.BaseURL+"/api/v1/user/applications/oauth2", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(g.Username, g.Password)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("gitea: create oauth app: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("gitea: create oauth app status %d: %s", resp.StatusCode, string(b))
	}

	var out struct {
		ID           int64  `json:"id"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("gitea: decode oauth app response: %v", err)
	}
	if out.ClientID == "" || out.ClientSecret == "" {
		t.Fatalf("gitea: empty client_id or client_secret in oauth app response")
	}
	return &GiteaOAuthApp{ID: out.ID, ClientID: out.ClientID, ClientSecret: out.ClientSecret}
}

// DeleteOAuthApp removes a previously-created OAuth2 application by ID.
// Best-effort: a 404 is treated as success so cleanup is idempotent.
func (g *GiteaBootstrap) DeleteOAuthApp(t *testing.T, id int64) {
	t.Helper()

	url := fmt.Sprintf("%s/api/v1/user/applications/oauth2/%d", g.BaseURL, id)
	req, _ := http.NewRequest(http.MethodDelete, url, nil)
	req.SetBasicAuth(g.Username, g.Password)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("gitea: delete oauth app %d: %v", id, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(resp.Body)
		t.Logf("gitea: delete oauth app %d: status %d: %s", id, resp.StatusCode, string(b))
	}
}
