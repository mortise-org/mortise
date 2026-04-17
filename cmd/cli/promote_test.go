package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPromote_PostsToCorrectPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects/myproject/apps/web/promote"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		var req promoteRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.From != "staging" {
			t.Errorf("expected from 'staging', got %q", req.From)
		}
		if req.To != "production" {
			t.Errorf("expected to 'production', got %q", req.To)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "promoted"})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	if err := c.Promote(c.ResolveProject(""), "web", "staging", "production"); err != nil {
		t.Fatal(err)
	}
}

func TestPromoteCmd_RequiresFromAndTo(t *testing.T) {
	cmd := newPromoteCmd()
	cmd.SetArgs([]string{"web", "--from", "staging"})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error when --to is not set")
	}

	cmd = newPromoteCmd()
	cmd.SetArgs([]string{"web", "--to", "production"})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error when --from is not set")
	}
}
