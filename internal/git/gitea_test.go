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
