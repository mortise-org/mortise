package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

func TestRollback_PostsToCorrectPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects/myproject/apps/web/rollback"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		var req rollbackRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Environment != "production" {
			t.Errorf("expected env 'production', got %q", req.Environment)
		}
		if req.Index != 1 {
			t.Errorf("expected index 1, got %d", req.Index)
		}
		_ = json.NewEncoder(w).Encode(mortisev1alpha1.DeployRecord{
			Image: "nginx:1.26",
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	record, err := c.Rollback(c.ResolveProject(""), "web", "production", 1)
	if err != nil {
		t.Fatal(err)
	}
	if record.Image != "nginx:1.26" {
		t.Errorf("expected image nginx:1.26, got %s", record.Image)
	}
}

func TestRollbackCmd_RequiresEnv(t *testing.T) {
	cmd := newRollbackCmd()
	cmd.SetArgs([]string{"web"})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error when --env is not set")
	}
}
