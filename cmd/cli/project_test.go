package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectList_GetsAPIProjects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode([]ProjectResponse{
			{Name: "default", Namespace: "project-default", Phase: "Ready", AppCount: 2},
		})
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: "t", HTTPClient: srv.Client()}
	list, err := c.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "default" {
		t.Fatalf("unexpected list: %+v", list)
	}
}

func TestProjectCreate_PostsName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		var req createProjectRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Name != "new-proj" {
			t.Errorf("expected name 'new-proj', got %q", req.Name)
		}
		if req.Description != "a description" {
			t.Errorf("expected description 'a description', got %q", req.Description)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(ProjectResponse{Name: req.Name, Namespace: "project-new-proj"})
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: "t", HTTPClient: srv.Client()}
	p, err := c.CreateProject("new-proj", "a description")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "new-proj" {
		t.Errorf("unexpected response name: %q", p.Name)
	}
}

func TestProjectDelete_DeletesByName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects/doomed"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodDelete {
			t.Errorf("unexpected method: %s", r.Method)
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "terminating"})
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: "t", HTTPClient: srv.Client()}
	if err := c.DeleteProject("doomed"); err != nil {
		t.Fatal(err)
	}
}

func TestProjectUse_UpdatesConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// seed a config so loadConfig has something to read
	dir := filepath.Join(tmp, ".config", "mortise")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(&Config{ServerURL: "http://srv", Token: "t", CurrentProject: "default"}); err != nil {
		t.Fatal(err)
	}

	use := newProjectUseCmd()
	use.SetArgs([]string{"infra"})
	if err := use.Execute(); err != nil {
		t.Fatalf("project use failed: %v", err)
	}

	loaded, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.CurrentProject != "infra" {
		t.Errorf("expected current_project=infra, got %q", loaded.CurrentProject)
	}
}

func TestProjectSubcommands(t *testing.T) {
	p := newProjectCmd()
	subs := map[string]bool{}
	for _, c := range p.Commands() {
		subs[c.Name()] = true
	}
	for _, name := range []string{"list", "create", "delete", "use", "show"} {
		if !subs[name] {
			t.Errorf("missing project subcommand: %s", name)
		}
	}
}
