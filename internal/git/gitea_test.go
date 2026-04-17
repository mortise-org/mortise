package git

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitea_RegisterWebhook_CreatesHook(t *testing.T) {
	var gotBody map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Gitea SDK hits POST /api/v1/repos/{owner}/{repo}/hooks
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/octo/app/hooks" {
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &gotBody)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":     1,
				"active": true,
				"type":   "gitea",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	api, err := NewGiteaAPI(srv.URL, "test-token", "test-secret")
	if err != nil {
		t.Fatalf("NewGiteaAPI: %v", err)
	}

	err = api.RegisterWebhook(context.Background(), "octo/app", WebhookConfig{
		URL:    "https://mortise.example.com/webhook",
		Secret: "wh-secret",
		Events: []string{"push", "pull_request"},
	})
	if err != nil {
		t.Fatalf("RegisterWebhook: %v", err)
	}

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
}

func TestGitea_PostCommitStatus(t *testing.T) {
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
				if r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/octo/app/statuses/sha789" {
					body, _ := io.ReadAll(r.Body)
					json.Unmarshal(body, &gotBody)
					w.WriteHeader(http.StatusCreated)
					json.NewEncoder(w).Encode(map[string]interface{}{
						"id":     1,
						"status": tt.wantState,
					})
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer srv.Close()

			api, err := NewGiteaAPI(srv.URL, "test-token", "test-secret")
			if err != nil {
				t.Fatalf("NewGiteaAPI: %v", err)
			}

			err = api.PostCommitStatus(context.Background(), "octo/app", "sha789", CommitStatus{
				State:       tt.state,
				TargetURL:   "https://mortise.example.com/builds/7",
				Description: "Build done",
				Context:     "mortise/build",
			})
			if err != nil {
				t.Fatalf("PostCommitStatus: %v", err)
			}

			if gotBody["state"] != tt.wantState {
				t.Errorf("state: got %v, want %s", gotBody["state"], tt.wantState)
			}
			if gotBody["target_url"] != "https://mortise.example.com/builds/7" {
				t.Errorf("target_url: got %v", gotBody["target_url"])
			}
			if gotBody["description"] != "Build done" {
				t.Errorf("description: got %v", gotBody["description"])
			}
			if gotBody["context"] != "mortise/build" {
				t.Errorf("context: got %v", gotBody["context"])
			}
		})
	}
}

func TestGitea_ListRepos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/user/repos" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]map[string]interface{}{
				{
					"id":             1,
					"full_name":      "octo/app",
					"name":           "app",
					"description":    "A test app",
					"default_branch": "main",
					"clone_url":      "https://gitea.example.com/octo/app.git",
					"updated_at":     "2025-02-20T14:00:00Z",
					"language":       "Rust",
					"private":        false,
					"owner":          map[string]interface{}{"login": "octo"},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	api, err := NewGiteaAPI(srv.URL, "test-token", "test-secret")
	if err != nil {
		t.Fatalf("NewGiteaAPI: %v", err)
	}

	repos, err := api.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(repos))
	}
	if repos[0].FullName != "octo/app" {
		t.Errorf("FullName: got %q", repos[0].FullName)
	}
	if repos[0].Language != "Rust" {
		t.Errorf("Language: got %q", repos[0].Language)
	}
	if repos[0].Private != false {
		t.Errorf("Private: expected false")
	}
}

func TestGitea_ListBranches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/octo/app/branches":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]map[string]interface{}{
				{"name": "main"},
				{"name": "feature-y"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/octo/app":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":             1,
				"name":           "app",
				"full_name":      "octo/app",
				"default_branch": "main",
				"owner":          map[string]interface{}{"login": "octo"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	api, err := NewGiteaAPI(srv.URL, "test-token", "test-secret")
	if err != nil {
		t.Fatalf("NewGiteaAPI: %v", err)
	}

	branches, err := api.ListBranches(context.Background(), "octo/app")
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	if len(branches) != 2 {
		t.Fatalf("expected 2 branches, got %d", len(branches))
	}
	if branches[0].Name != "main" || !branches[0].Default {
		t.Errorf("branches[0]: got %+v", branches[0])
	}
	if branches[1].Name != "feature-y" || branches[1].Default {
		t.Errorf("branches[1]: got %+v", branches[1])
	}
}

func TestGitea_ResolveCloneCredentials(t *testing.T) {
	api := &GiteaAPI{token: "gitea-tok-123"}

	creds, err := api.ResolveCloneCredentials(context.Background(), "octo/app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.Token != "gitea-tok-123" {
		t.Errorf("expected token gitea-tok-123, got %q", creds.Token)
	}
}
