package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTokenCreate_PostsCorrectly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects/myproject/apps/web/tokens"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		var req cliCreateTokenRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Name != "github-ci" {
			t.Errorf("expected name github-ci, got %q", req.Name)
		}
		if req.Environment != "production" {
			t.Errorf("expected environment production, got %q", req.Environment)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(TokenResponse{
			Token:       "mrt_abc123",
			Name:        "github-ci",
			Environment: "production",
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	resp, err := c.CreateToken(c.ResolveProject(""), "web", "production", "github-ci")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Token != "mrt_abc123" {
		t.Errorf("expected token mrt_abc123, got %q", resp.Token)
	}
}

func TestTokenList_GetsCorrectly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects/myproject/apps/web/tokens"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		_ = json.NewEncoder(w).Encode([]TokenResponse{
			{Name: "ci", Environment: "production", CreatedAt: "2026-01-01T00:00:00Z"},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	tokens, err := c.ListTokens(c.ResolveProject(""), "web")
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].Name != "ci" {
		t.Errorf("unexpected tokens: %+v", tokens)
	}
}

func TestTokenRevoke_DeletesCorrectly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/tokens/github-ci") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodDelete {
			t.Errorf("unexpected method: %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	if err := c.RevokeToken(c.ResolveProject(""), "web", "github-ci"); err != nil {
		t.Fatal(err)
	}
}

func TestTokenSubcommands(t *testing.T) {
	token := newTokenCmd()
	subs := map[string]bool{}
	for _, c := range token.Commands() {
		subs[c.Name()] = true
	}
	for _, name := range []string{"create", "list", "revoke"} {
		if !subs[name] {
			t.Errorf("missing token subcommand: %s", name)
		}
	}
}
