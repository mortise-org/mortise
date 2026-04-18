package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecretList_QueriesCorrectPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects/myproject/apps/web/secrets"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode([]SecretResponse{
			{Name: "API_KEY", Keys: []string{"API_KEY"}},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	secrets, err := c.ListSecrets(c.ResolveProject(""), "web")
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 1 || secrets[0].Name != "API_KEY" {
		t.Errorf("unexpected secrets: %+v", secrets)
	}
}

func TestSecretSet_PostsCorrectBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects/myproject/apps/web/secrets"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		var req createSecretRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Name != "DB_PASS" {
			t.Errorf("expected name 'DB_PASS', got %q", req.Name)
		}
		if req.Data["DB_PASS"] != "s3cret" {
			t.Errorf("expected value 's3cret', got %q", req.Data["DB_PASS"])
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	if err := c.SetSecret(c.ResolveProject(""), "web", "DB_PASS", "s3cret"); err != nil {
		t.Fatal(err)
	}
}

func TestSecretDelete_DeletesCorrectPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects/myproject/apps/web/secrets/DB_PASS"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodDelete {
			t.Errorf("unexpected method: %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	if err := c.DeleteSecret(c.ResolveProject(""), "web", "DB_PASS"); err != nil {
		t.Fatal(err)
	}
}

func TestSecretSubcommands(t *testing.T) {
	secret := newSecretCmd()
	subs := map[string]bool{}
	for _, c := range secret.Commands() {
		subs[c.Name()] = true
	}
	for _, name := range []string{"list", "set", "delete"} {
		if !subs[name] {
			t.Errorf("missing secret subcommand: %s", name)
		}
	}
}
