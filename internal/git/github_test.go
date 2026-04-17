package git

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// newTestGitHubAPI creates a GitHubAPI pointing at the given httptest server.
// The server URL is used as a GHE base URL so the SDK hits {url}/api/v3/...
func newTestGitHubAPI(t *testing.T, serverURL string) *GitHubAPI {
	t.Helper()
	api, err := NewGitHubAPI(serverURL, "test-token", "test-secret")
	if err != nil {
		t.Fatalf("NewGitHubAPI: %v", err)
	}
	return api
}

func TestGitHub_RegisterWebhook_CreatesHookWhenNoneExist(t *testing.T) {
	var mu sync.Mutex
	var gotMethod string
	var gotPath string
	var gotBody map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		// The SDK hits POST /api/v3/repos/{owner}/{repo}/hooks
		if r.Method == http.MethodPost && r.URL.Path == "/api/v3/repos/octo/repo/hooks" {
			gotMethod = r.Method
			gotPath = r.URL.Path
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &gotBody)

			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":     1,
				"active": true,
				"events": []string{"push", "pull_request"},
				"config": map[string]string{
					"url":          "https://mortise.example.com/webhook",
					"content_type": "json",
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	api := newTestGitHubAPI(t, srv.URL)
	err := api.RegisterWebhook(context.Background(), "octo/repo", WebhookConfig{
		URL:    "https://mortise.example.com/webhook",
		Secret: "wh-secret",
		Events: []string{"push", "pull_request"},
	})
	if err != nil {
		t.Fatalf("RegisterWebhook: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/api/v3/repos/octo/repo/hooks" {
		t.Errorf("unexpected path: %s", gotPath)
	}

	// Verify hook config fields in the request body.
	cfg, ok := gotBody["config"].(map[string]interface{})
	if !ok {
		t.Fatalf("config not found in body: %v", gotBody)
	}
	if cfg["url"] != "https://mortise.example.com/webhook" {
		t.Errorf("unexpected hook URL: %v", cfg["url"])
	}
	if cfg["content_type"] != "json" {
		t.Errorf("unexpected content_type: %v", cfg["content_type"])
	}
	if cfg["secret"] != "wh-secret" {
		t.Errorf("unexpected secret: %v", cfg["secret"])
	}

	events, ok := gotBody["events"].([]interface{})
	if !ok {
		t.Fatalf("events not found in body: %v", gotBody)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events, got %d", len(events))
	}
}

func TestGitHub_RegisterWebhook_DefaultEvents(t *testing.T) {
	var gotBody map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &gotBody)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 1})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	api := newTestGitHubAPI(t, srv.URL)
	err := api.RegisterWebhook(context.Background(), "octo/repo", WebhookConfig{
		URL:    "https://mortise.example.com/webhook",
		Secret: "wh-secret",
		// No Events specified — should default to push + pull_request.
	})
	if err != nil {
		t.Fatalf("RegisterWebhook: %v", err)
	}

	events, ok := gotBody["events"].([]interface{})
	if !ok {
		t.Fatalf("events not found in body: %v", gotBody)
	}
	want := map[string]bool{"push": true, "pull_request": true}
	for _, e := range events {
		s, _ := e.(string)
		if !want[s] {
			t.Errorf("unexpected event: %s", s)
		}
	}
	if len(events) != 2 {
		t.Errorf("expected 2 default events, got %d", len(events))
	}
}

func TestGitHub_PostCommitStatus(t *testing.T) {
	tests := []struct {
		name      string
		state     CommitStatusState
		wantState string
	}{
		{"pending", StatusPending, "pending"},
		{"success", StatusSuccess, "success"},
		{"failure", StatusFailure, "failure"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody map[string]interface{}

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path == "/api/v3/repos/octo/repo/statuses/abc123" {
					body, _ := io.ReadAll(r.Body)
					json.Unmarshal(body, &gotBody)
					w.WriteHeader(http.StatusCreated)
					json.NewEncoder(w).Encode(map[string]interface{}{"id": 1, "state": tt.wantState})
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer srv.Close()

			api := newTestGitHubAPI(t, srv.URL)
			err := api.PostCommitStatus(context.Background(), "octo/repo", "abc123", CommitStatus{
				State:       tt.state,
				TargetURL:   "https://mortise.example.com/builds/42",
				Description: "Build passed",
				Context:     "mortise/build",
			})
			if err != nil {
				t.Fatalf("PostCommitStatus: %v", err)
			}

			if gotBody["state"] != tt.wantState {
				t.Errorf("state: got %v, want %s", gotBody["state"], tt.wantState)
			}
			if gotBody["target_url"] != "https://mortise.example.com/builds/42" {
				t.Errorf("target_url: got %v", gotBody["target_url"])
			}
			if gotBody["description"] != "Build passed" {
				t.Errorf("description: got %v", gotBody["description"])
			}
			if gotBody["context"] != "mortise/build" {
				t.Errorf("context: got %v", gotBody["context"])
			}
		})
	}
}

func TestGitHub_VerifyWebhookSignature_Valid(t *testing.T) {
	const secret = "hmac-secret"
	api := &GitHubAPI{secret: secret}

	body := []byte(`{"action":"opened","number":1}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	hdr := http.Header{"X-Hub-Signature-256": []string{sig}}
	if err := api.VerifyWebhookSignature(body, hdr); err != nil {
		t.Errorf("expected nil, got: %v", err)
	}
}

func TestGitHub_VerifyWebhookSignature_Invalid(t *testing.T) {
	api := &GitHubAPI{secret: "real-secret"}
	body := []byte(`{"action":"opened"}`)

	hdr := http.Header{"X-Hub-Signature-256": []string{"sha256=0000000000000000000000000000000000000000000000000000000000000000"}}
	if err := api.VerifyWebhookSignature(body, hdr); err == nil {
		t.Error("expected error for invalid signature")
	}
}

func TestGitHub_VerifyWebhookSignature_MissingHeader(t *testing.T) {
	api := &GitHubAPI{secret: "some-secret"}
	body := []byte(`{}`)

	if err := api.VerifyWebhookSignature(body, http.Header{}); err == nil {
		t.Error("expected error for missing header")
	}
}

func TestGitHub_ResolveCloneCredentials(t *testing.T) {
	api := &GitHubAPI{token: "ghp_testtoken123"}

	creds, err := api.ResolveCloneCredentials(context.Background(), "octo/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.Token != "ghp_testtoken123" {
		t.Errorf("expected token ghp_testtoken123, got %q", creds.Token)
	}
}

func TestGitHub_ListRepos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v3/user/repos" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]map[string]interface{}{
				{
					"id":             1,
					"full_name":      "octo/hello-world",
					"name":           "hello-world",
					"description":    "My first repo",
					"default_branch": "main",
					"clone_url":      "https://github.com/octo/hello-world.git",
					"updated_at":     "2025-03-01T12:00:00Z",
					"language":       "Go",
					"private":        false,
					"owner":          map[string]interface{}{"login": "octo"},
				},
				{
					"id":             2,
					"full_name":      "octo/private-app",
					"name":           "private-app",
					"description":    "",
					"default_branch": "develop",
					"clone_url":      "https://github.com/octo/private-app.git",
					"updated_at":     "2025-04-01T08:30:00Z",
					"language":       "TypeScript",
					"private":        true,
					"owner":          map[string]interface{}{"login": "octo"},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	api := newTestGitHubAPI(t, srv.URL)
	repos, err := api.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(repos))
	}
	if repos[0].FullName != "octo/hello-world" {
		t.Errorf("repos[0].FullName: got %q", repos[0].FullName)
	}
	if repos[0].Language != "Go" {
		t.Errorf("repos[0].Language: got %q", repos[0].Language)
	}
	if repos[0].Private != false {
		t.Errorf("repos[0].Private: expected false")
	}
	if repos[1].Private != true {
		t.Errorf("repos[1].Private: expected true")
	}
	if repos[1].DefaultBranch != "develop" {
		t.Errorf("repos[1].DefaultBranch: got %q", repos[1].DefaultBranch)
	}
}

func TestGitHub_ListBranches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/repos/octo/repo/branches":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]map[string]interface{}{
				{"name": "main", "protected": true},
				{"name": "feature-x", "protected": false},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/repos/octo/repo":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":             1,
				"name":           "repo",
				"full_name":      "octo/repo",
				"default_branch": "main",
				"owner":          map[string]interface{}{"login": "octo"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	api := newTestGitHubAPI(t, srv.URL)
	branches, err := api.ListBranches(context.Background(), "octo/repo")
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	if len(branches) != 2 {
		t.Fatalf("expected 2 branches, got %d", len(branches))
	}
	if branches[0].Name != "main" || !branches[0].Default {
		t.Errorf("branches[0]: got %+v, want main/default=true", branches[0])
	}
	if branches[1].Name != "feature-x" || branches[1].Default {
		t.Errorf("branches[1]: got %+v, want feature-x/default=false", branches[1])
	}
}

func TestGitHub_RegisterWebhook_InvalidRepo(t *testing.T) {
	api := &GitHubAPI{}
	err := api.RegisterWebhook(context.Background(), "noslash", WebhookConfig{URL: "https://example.com"})
	if err == nil {
		t.Error("expected error for invalid repo format")
	}
}

func TestGitHub_PostCommitStatus_InvalidRepo(t *testing.T) {
	api := &GitHubAPI{}
	err := api.PostCommitStatus(context.Background(), "noslash", "abc", CommitStatus{State: StatusSuccess})
	if err == nil {
		t.Error("expected error for invalid repo format")
	}
}
