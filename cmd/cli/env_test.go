package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnvGet_QueriesCorrectly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects/myproject/apps/web/env"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if got := r.URL.Query().Get("environment"); got != "production" {
			t.Errorf("expected environment=production, got %q", got)
		}
		_ = json.NewEncoder(w).Encode([]EnvVarResponse{
			{Name: "PORT", Value: "3000"},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	vars, err := c.GetEnv(c.ResolveProject(""), "web", "production")
	if err != nil {
		t.Fatal(err)
	}
	if len(vars) != 1 || vars[0].Name != "PORT" {
		t.Errorf("unexpected vars: %+v", vars)
	}
}

func TestEnvPatch_SendsCorrectBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("unexpected method: %s", r.Method)
		}
		var req cliPatchEnvRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Set["PORT"] != "8080" {
			t.Errorf("expected PORT=8080 in set, got %v", req.Set)
		}
		if len(req.Unset) != 1 || req.Unset[0] != "OLD" {
			t.Errorf("expected [OLD] in unset, got %v", req.Unset)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.PatchEnv(c.ResolveProject(""), "web", "production",
		map[string]string{"PORT": "8080"}, []string{"OLD"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEnvSubcommands(t *testing.T) {
	env := newEnvCmd()
	subs := map[string]bool{}
	for _, c := range env.Commands() {
		subs[c.Name()] = true
	}
	for _, name := range []string{"list", "set", "unset", "import", "pull"} {
		if !subs[name] {
			t.Errorf("missing env subcommand: %s", name)
		}
	}
}

func TestRootCommand_HasTokenAndEnv(t *testing.T) {
	root := newRootCmd()
	found := map[string]bool{"token": false, "env": false}
	for _, sub := range root.Commands() {
		if _, ok := found[sub.Name()]; ok {
			found[sub.Name()] = true
		}
	}
	for name, ok := range found {
		if !ok {
			t.Errorf("missing subcommand: %s", name)
		}
	}
}
