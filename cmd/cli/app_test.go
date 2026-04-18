package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

// newTestClient returns a Client wired to the httptest server with a default
// "myproject" as the current project. Tests that need a different current
// project set the field directly after construction.
func newTestClient(srv *httptest.Server) *Client {
	return &Client{
		BaseURL:        srv.URL,
		Token:          "test-token",
		HTTPClient:     srv.Client(),
		currentProject: "myproject",
	}
}

func TestAppList_UsesProjectBase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects/myproject/apps"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
			t.Errorf("unexpected auth header: %s", auth)
		}
		_ = json.NewEncoder(w).Encode([]mortisev1alpha1.App{
			{
				Spec:   mortisev1alpha1.AppSpec{Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage}},
				Status: mortisev1alpha1.AppStatus{Phase: mortisev1alpha1.AppPhaseReady},
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	apps, err := c.ListApps(c.ResolveProject(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 {
		t.Fatalf("expected 1 app, got %d", len(apps))
	}
}

func TestAppList_FlagOverridesCurrentProject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/projects/infra/apps") {
			t.Errorf("expected /api/projects/infra/apps, got %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]mortisev1alpha1.App{})
	}))
	defer srv.Close()

	c := newTestClient(srv) // currentProject=myproject
	if _, err := c.ListApps(c.ResolveProject("infra")); err != nil {
		t.Fatal(err)
	}
}

func TestAppCreate_PostsToProjectApps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects/myproject/apps"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		var req CreateAppRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Name != "myapp" {
			t.Errorf("expected name 'myapp', got %q", req.Name)
		}
		if req.Spec.Source.Type != mortisev1alpha1.SourceTypeImage {
			t.Errorf("expected source type 'image', got %q", req.Spec.Source.Type)
		}
		if req.Spec.Source.Image != "nginx:1.27" {
			t.Errorf("expected image 'nginx:1.27', got %q", req.Spec.Source.Image)
		}
		_ = json.NewEncoder(w).Encode(mortisev1alpha1.App{Spec: req.Spec})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.CreateApp(c.ResolveProject(""), CreateAppRequest{
		Name: "myapp",
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.27"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAppDelete_DeletesNestedPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects/myproject/apps/myapp"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodDelete {
			t.Errorf("unexpected method: %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	if err := c.DeleteApp(c.ResolveProject(""), "myapp"); err != nil {
		t.Fatal(err)
	}
}

func TestDeploy_PostsToProjectAppsDeploy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects/myproject/apps/web/deploy"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		var req deployRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Environment != "production" || req.Image != "nginx:1.27" {
			t.Errorf("unexpected body: %+v", req)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	if err := c.Deploy(c.ResolveProject(""), "web", "production", "nginx:1.27"); err != nil {
		t.Fatal(err)
	}
}

func TestResolveProject_FlagWins(t *testing.T) {
	c := &Client{currentProject: "fromconfig"}
	if got := c.ResolveProject("fromflag"); got != "fromflag" {
		t.Errorf("expected flag to win, got %q", got)
	}
}

func TestResolveProject_FallsBackToCurrent(t *testing.T) {
	c := &Client{currentProject: "fromconfig"}
	if got := c.ResolveProject(""); got != "fromconfig" {
		t.Errorf("expected fallback to current project, got %q", got)
	}
}

func TestConfig_ProjectFallsBackToDefault(t *testing.T) {
	cfg := &Config{}
	if got := cfg.Project(); got != defaultProject {
		t.Errorf("expected %q when current_project unset, got %q", defaultProject, got)
	}
}

func TestConfigLoadSave(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "config.yaml")

	t.Setenv("HOME", tmp)

	// Create the config dir structure that configPath expects
	dir := filepath.Join(tmp, ".config", "mortise")
	_ = os.MkdirAll(dir, 0o700)

	cfg := &Config{
		ServerURL:      "http://localhost:8080",
		Token:          "abc",
		CurrentProject: "infra",
	}
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ServerURL != cfg.ServerURL {
		t.Errorf("server_url: got %q, want %q", loaded.ServerURL, cfg.ServerURL)
	}
	if loaded.Token != cfg.Token {
		t.Errorf("token: got %q, want %q", loaded.Token, cfg.Token)
	}
	if loaded.CurrentProject != cfg.CurrentProject {
		t.Errorf("current_project: got %q, want %q", loaded.CurrentProject, cfg.CurrentProject)
	}

	_ = os.Remove(p)
}

func TestConfigLoadMissing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerURL != "" {
		t.Errorf("expected empty server_url, got %q", cfg.ServerURL)
	}
	// Project() falls back to default even when the field is unset on disk.
	if got := cfg.Project(); got != defaultProject {
		t.Errorf("expected %q fallback, got %q", defaultProject, got)
	}
}

func TestRootCommandStructure(t *testing.T) {
	root := newRootCmd()
	if root.Use != "mortise" {
		t.Errorf("expected root command 'mortise', got %q", root.Use)
	}

	expected := map[string]bool{
		"login": false, "project": false, "app": false, "deploy": false, "logs": false, "status": false,
		"secret": false, "git-provider": false, "platform": false, "repo": false,
	}
	for _, sub := range root.Commands() {
		if _, ok := expected[sub.Name()]; ok {
			expected[sub.Name()] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("missing subcommand: %s", name)
		}
	}
}

func TestAppUpdate_PutsToCorrectPath(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects/myproject/apps/web":
			// Return current app state
			_ = json.NewEncoder(w).Encode(mortisev1alpha1.App{
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "nginx:1.27",
					},
				},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/api/projects/myproject/apps/web":
			calls++
			var spec mortisev1alpha1.AppSpec
			_ = json.NewDecoder(r.Body).Decode(&spec)
			if spec.Source.Image != "nginx:1.27" {
				t.Errorf("expected image preserved as nginx:1.27, got %q", spec.Source.Image)
			}
			_ = json.NewEncoder(w).Encode(mortisev1alpha1.App{Spec: spec})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.UpdateApp(c.ResolveProject(""), "web", mortisev1alpha1.AppSpec{
		Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.27"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("expected 1 PUT call, got %d", calls)
	}
}

func TestAppSubcommands(t *testing.T) {
	app := newAppCmd()
	subs := map[string]bool{}
	for _, c := range app.Commands() {
		subs[c.Name()] = true
	}
	for _, name := range []string{"list", "create", "update", "delete"} {
		if !subs[name] {
			t.Errorf("missing app subcommand: %s", name)
		}
	}
}

func TestAppSubcommands_HaveProjectFlag(t *testing.T) {
	app := newAppCmd()
	for _, c := range app.Commands() {
		if c.Flags().Lookup("project") == nil {
			t.Errorf("app %s missing --project flag", c.Name())
		}
	}
}

func TestAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: "bad", HTTPClient: srv.Client()}
	err := c.doJSON("GET", srv.URL+"/api/projects", nil, nil)
	if err == nil {
		t.Fatal("expected error for 401")
	}
}
