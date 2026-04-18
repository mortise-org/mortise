package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPlatformGet_QueriesCorrectPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/platform"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(PlatformResponse{
			Domain: "example.com",
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	p, err := c.GetPlatform()
	if err != nil {
		t.Fatal(err)
	}
	if p.Domain != "example.com" {
		t.Errorf("unexpected domain: %q", p.Domain)
	}
}

func TestPlatformPatch_SendsCorrectBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/platform"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodPatch {
			t.Errorf("unexpected method: %s", r.Method)
		}
		var req PlatformPatchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Domain != "new.example.com" {
			t.Errorf("expected domain 'new.example.com', got %q", req.Domain)
		}
		_ = json.NewEncoder(w).Encode(PlatformResponse{Domain: "new.example.com"})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	p, err := c.PatchPlatform(PlatformPatchRequest{Domain: "new.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Domain != "new.example.com" {
		t.Errorf("unexpected domain: %q", p.Domain)
	}
}

func TestPlatformSubcommands(t *testing.T) {
	plat := newPlatformCmd()
	subs := map[string]bool{}
	for _, c := range plat.Commands() {
		subs[c.Name()] = true
	}
	for _, name := range []string{"get", "set"} {
		if !subs[name] {
			t.Errorf("missing platform subcommand: %s", name)
		}
	}
}
