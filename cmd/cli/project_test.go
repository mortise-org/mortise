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
			{Name: "default", Namespace: "pj-default", Phase: "Ready", AppCount: 2},
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
		_ = json.NewEncoder(w).Encode(ProjectResponse{Name: req.Name, Namespace: "pj-new-proj"})
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
	for _, name := range []string{"list", "create", "delete", "use", "show", "env"} {
		if !subs[name] {
			t.Errorf("missing project subcommand: %s", name)
		}
	}
}

func TestProjectEnvSubcommands(t *testing.T) {
	env := newProjectEnvCmd()
	subs := map[string]bool{}
	for _, c := range env.Commands() {
		subs[c.Name()] = true
	}
	for _, name := range []string{"list", "create", "delete", "rename"} {
		if !subs[name] {
			t.Errorf("missing project env subcommand: %s", name)
		}
	}
}

func TestProjectEnvList_GetsAPIEnvs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects/demo/environments"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode([]ProjectEnvResponse{
			{Name: "production", DisplayOrder: 0, Health: "healthy"},
			{Name: "staging", DisplayOrder: 1, Health: "unknown"},
		})
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: "t", HTTPClient: srv.Client()}
	envs, err := c.ListProjectEnvs("demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 2 || envs[0].Name != "production" || envs[1].Name != "staging" {
		t.Fatalf("unexpected envs: %+v", envs)
	}
}

func TestProjectEnvCreate_PostsName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects/demo/environments"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		var req createProjectEnvRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Name != "staging" || req.DisplayOrder != 2 {
			t.Errorf("unexpected body: %+v", req)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(ProjectEnvResponse{Name: req.Name, DisplayOrder: req.DisplayOrder})
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: "t", HTTPClient: srv.Client()}
	env, err := c.CreateProjectEnv("demo", "staging", 2)
	if err != nil {
		t.Fatal(err)
	}
	if env.Name != "staging" {
		t.Errorf("unexpected env name: %q", env.Name)
	}
}

func TestProjectEnvRename_PatchesName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects/demo/environments/staging"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodPatch {
			t.Errorf("unexpected method: %s", r.Method)
		}
		var req patchProjectEnvRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Name == nil || *req.Name != "stage" {
			t.Errorf("unexpected body: %+v", req)
		}
		_ = json.NewEncoder(w).Encode(ProjectEnvResponse{Name: "stage"})
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: "t", HTTPClient: srv.Client()}
	env, err := c.RenameProjectEnv("demo", "staging", "stage")
	if err != nil {
		t.Fatal(err)
	}
	if env.Name != "stage" {
		t.Errorf("unexpected env name: %q", env.Name)
	}
}

func TestProjectEnvDelete_Deletes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects/demo/environments/staging"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodDelete {
			t.Errorf("unexpected method: %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: "t", HTTPClient: srv.Client()}
	if err := c.DeleteProjectEnv("demo", "staging"); err != nil {
		t.Fatal(err)
	}
}
