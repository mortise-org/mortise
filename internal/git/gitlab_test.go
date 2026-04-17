package git

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestGitLabAPI creates a GitLabAPI pointing at the given httptest server.
func newTestGitLabAPI(t *testing.T, serverURL string) *GitLabAPI {
	t.Helper()
	api, err := NewGitLabAPI(serverURL, "test-token", "test-secret")
	if err != nil {
		t.Fatalf("NewGitLabAPI: %v", err)
	}
	return api
}

func TestGitLab_RegisterWebhook_CreatesHook(t *testing.T) {
	var gotBody map[string]interface{}
	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GitLab SDK hits POST /api/v4/projects/{id}/hooks
		if r.Method == http.MethodPost && r.URL.Path == "/api/v4/projects/myorg/myrepo/hooks" {
			gotPath = r.URL.Path
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &gotBody)

			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":                    1,
				"url":                   "https://mortise.example.com/webhook",
				"push_events":           true,
				"merge_requests_events": true,
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	api := newTestGitLabAPI(t, srv.URL)
	err := api.RegisterWebhook(context.Background(), "myorg/myrepo", WebhookConfig{
		URL:    "https://mortise.example.com/webhook",
		Secret: "wh-secret",
	})
	if err != nil {
		t.Fatalf("RegisterWebhook: %v", err)
	}

	if gotPath != "/api/v4/projects/myorg/myrepo/hooks" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if gotBody["url"] != "https://mortise.example.com/webhook" {
		t.Errorf("unexpected URL: %v", gotBody["url"])
	}
	// GitLab webhook token is sent as "token" in the body.
	if gotBody["token"] != "wh-secret" {
		t.Errorf("unexpected token: %v", gotBody["token"])
	}
	if gotBody["push_events"] != true {
		t.Errorf("expected push_events=true, got %v", gotBody["push_events"])
	}
	if gotBody["merge_requests_events"] != true {
		t.Errorf("expected merge_requests_events=true, got %v", gotBody["merge_requests_events"])
	}
}

func TestGitLab_PostCommitStatus(t *testing.T) {
	tests := []struct {
		name      string
		state     CommitStatusState
		wantState string
	}{
		{"pending", StatusPending, "pending"},
		{"success", StatusSuccess, "success"},
		{"failure maps to failed", StatusFailure, "failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody map[string]interface{}
			var gotPath string

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// POST /api/v4/projects/{id}/statuses/{sha}
				if r.Method == http.MethodPost && r.URL.Path == "/api/v4/projects/myorg/myrepo/statuses/def456" {
					gotPath = r.URL.Path
					body, _ := io.ReadAll(r.Body)
					json.Unmarshal(body, &gotBody)

					w.WriteHeader(http.StatusCreated)
					json.NewEncoder(w).Encode(map[string]interface{}{
						"id":     1,
						"sha":    "def456",
						"status": tt.wantState,
					})
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer srv.Close()

			api := newTestGitLabAPI(t, srv.URL)
			err := api.PostCommitStatus(context.Background(), "myorg/myrepo", "def456", CommitStatus{
				State:       tt.state,
				TargetURL:   "https://mortise.example.com/builds/99",
				Description: "Pipeline complete",
				Context:     "mortise/ci",
			})
			if err != nil {
				t.Fatalf("PostCommitStatus: %v", err)
			}

			if gotPath != "/api/v4/projects/myorg/myrepo/statuses/def456" {
				t.Errorf("unexpected path: %s", gotPath)
			}
			if gotBody["state"] != tt.wantState {
				t.Errorf("state: got %v, want %s", gotBody["state"], tt.wantState)
			}
			if gotBody["target_url"] != "https://mortise.example.com/builds/99" {
				t.Errorf("target_url: got %v", gotBody["target_url"])
			}
			if gotBody["description"] != "Pipeline complete" {
				t.Errorf("description: got %v", gotBody["description"])
			}
			if gotBody["name"] != "mortise/ci" {
				t.Errorf("name (context): got %v", gotBody["name"])
			}
		})
	}
}

func TestGitLab_ListRepos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v4/projects" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]map[string]interface{}{
				{
					"id":                  1,
					"path_with_namespace": "myorg/myrepo",
					"name":                "myrepo",
					"description":         "My repo",
					"default_branch":      "main",
					"http_url_to_repo":    "https://gitlab.com/myorg/myrepo.git",
					"last_activity_at":    "2025-03-15T10:00:00Z",
					"visibility":          "private",
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	api := newTestGitLabAPI(t, srv.URL)
	repos, err := api.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(repos))
	}
	if repos[0].FullName != "myorg/myrepo" {
		t.Errorf("FullName: got %q", repos[0].FullName)
	}
	if repos[0].CloneURL != "https://gitlab.com/myorg/myrepo.git" {
		t.Errorf("CloneURL: got %q", repos[0].CloneURL)
	}
	if repos[0].Private != true {
		t.Errorf("Private: expected true")
	}
}

func TestGitLab_ListBranches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v4/projects/myorg/myrepo/repository/branches" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]map[string]interface{}{
				{"name": "main", "default": true, "protected": true},
				{"name": "dev", "default": false, "protected": false},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	api := newTestGitLabAPI(t, srv.URL)
	branches, err := api.ListBranches(context.Background(), "myorg/myrepo")
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	if len(branches) != 2 {
		t.Fatalf("expected 2 branches, got %d", len(branches))
	}
	if branches[0].Name != "main" || !branches[0].Default {
		t.Errorf("branches[0]: got %+v", branches[0])
	}
	if branches[1].Name != "dev" || branches[1].Default {
		t.Errorf("branches[1]: got %+v", branches[1])
	}
}

func TestGitLab_VerifyWebhookSignature_Valid(t *testing.T) {
	api := &GitLabAPI{secret: "gl-token-abc"}

	hdr := http.Header{"X-Gitlab-Token": []string{"gl-token-abc"}}
	if err := api.VerifyWebhookSignature(nil, hdr); err != nil {
		t.Errorf("expected nil, got: %v", err)
	}
}

func TestGitLab_VerifyWebhookSignature_Invalid(t *testing.T) {
	api := &GitLabAPI{secret: "correct-token"}

	hdr := http.Header{"X-Gitlab-Token": []string{"wrong-token"}}
	if err := api.VerifyWebhookSignature(nil, hdr); err == nil {
		t.Error("expected error for invalid token")
	}
}

func TestGitLab_VerifyWebhookSignature_MissingHeader(t *testing.T) {
	api := &GitLabAPI{secret: "some-token"}

	if err := api.VerifyWebhookSignature(nil, http.Header{}); err == nil {
		t.Error("expected error for missing header")
	}
}

func TestGitLab_ResolveCloneCredentials(t *testing.T) {
	api := &GitLabAPI{token: "glpat-testtoken"}

	creds, err := api.ResolveCloneCredentials(context.Background(), "myorg/myrepo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.Token != "glpat-testtoken" {
		t.Errorf("expected token glpat-testtoken, got %q", creds.Token)
	}
}

func TestGitLab_GitLabStateMapping(t *testing.T) {
	tests := []struct {
		input CommitStatusState
		want  string
	}{
		{StatusPending, "pending"},
		{StatusSuccess, "success"},
		{StatusFailure, "failed"},
		{CommitStatusState("unknown"), "pending"},
	}
	for _, tt := range tests {
		got := gitlabState(tt.input)
		if got != tt.want {
			t.Errorf("gitlabState(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
