package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestAppListCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps" || r.Method != "GET" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
			t.Errorf("unexpected auth header: %s", auth)
		}
		_ = json.NewEncoder(w).Encode(AppListResponse{
			Items: []AppResponse{
				{Name: "web", Source: AppSourceResp{Type: "image"}, Status: AppStatusResp{Phase: "Ready"}},
			},
		})
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: "test-token", HTTPClient: srv.Client()}
	var resp AppListResponse
	if err := c.doJSON("GET", "/api/apps", nil, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(resp.Items))
	}
	if resp.Items[0].Name != "web" {
		t.Errorf("expected name 'web', got %q", resp.Items[0].Name)
	}
}

func TestAppCreateCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps" || r.Method != "POST" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req CreateAppRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Name != "myapp" {
			t.Errorf("expected name 'myapp', got %q", req.Name)
		}
		if req.Source.Type != "image" {
			t.Errorf("expected source type 'image', got %q", req.Source.Type)
		}
		_ = json.NewEncoder(w).Encode(AppResponse{Name: req.Name, Source: AppSourceResp{Type: req.Source.Type}})
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: "tok", HTTPClient: srv.Client()}
	var resp AppResponse
	if err := c.doJSON("POST", "/api/apps", CreateAppRequest{
		Name:   "myapp",
		Source: CreateAppSourceReq{Type: "image", Image: "nginx:1.27"},
	}, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Name != "myapp" {
		t.Errorf("expected 'myapp', got %q", resp.Name)
	}
}

func TestAppDeleteCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/myapp" || r.Method != "DELETE" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: "tok", HTTPClient: srv.Client()}
	if err := c.doJSON("DELETE", "/api/apps/myapp", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestConfigLoadSave(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "config.yaml")

	t.Setenv("HOME", tmp)

	// Create the config dir structure that configPath expects
	dir := filepath.Join(tmp, ".config", "mortise")
	_ = os.MkdirAll(dir, 0o700)

	cfg := &Config{ServerURL: "http://localhost:8080", Token: "abc"}
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

	// Clean up so we don't leave the file
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
}

func TestRootCommandStructure(t *testing.T) {
	root := newRootCmd()
	if root.Use != "mortise" {
		t.Errorf("expected root command 'mortise', got %q", root.Use)
	}

	expected := map[string]bool{
		"login": false, "app": false, "deploy": false, "logs": false, "status": false,
	}
	for _, sub := range root.Commands() {
		if _, ok := expected[sub.Use]; !ok {
			// Commands like "help" and "completion" are added by cobra
			continue
		}
		expected[sub.Use] = true
	}
	// app has Use="app" not "app [command]" so check by Name()
	for _, sub := range root.Commands() {
		delete(expected, sub.Name())
	}
	for name := range expected {
		t.Errorf("missing subcommand: %s", name)
	}
}

func TestAppSubcommands(t *testing.T) {
	app := newAppCmd()
	subs := map[string]bool{}
	for _, c := range app.Commands() {
		subs[c.Name()] = true
	}
	for _, name := range []string{"list", "create", "delete"} {
		if !subs[name] {
			t.Errorf("missing app subcommand: %s", name)
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
	err := c.doJSON("GET", "/api/apps", nil, nil)
	if err == nil {
		t.Fatal("expected error for 401")
	}
}
