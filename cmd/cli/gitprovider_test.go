package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitProviderList_QueriesCorrectPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/gitproviders"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode([]GitProviderSummary{
			{Name: "github-main", Type: "github", Host: "github.com", Phase: "Ready"},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	providers, err := c.ListGitProviders()
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 || providers[0].Name != "github-main" {
		t.Errorf("unexpected providers: %+v", providers)
	}
}

func TestGitProviderCreate_PostsCorrectBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/gitproviders"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		var req CreateGitProviderRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Name != "gh" || req.Type != "github" {
			t.Errorf("unexpected body: %+v", req)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.CreateGitProvider(CreateGitProviderRequest{
		Name: "gh", Type: "github", Host: "github.com",
		ClientID: "id", ClientSecret: "secret", WebhookSecret: "wh",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGitProviderDelete_DeletesCorrectPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/gitproviders/github-main"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodDelete {
			t.Errorf("unexpected method: %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	if err := c.DeleteGitProvider("github-main"); err != nil {
		t.Fatal(err)
	}
}

func TestGitProviderSubcommands(t *testing.T) {
	gp := newGitProviderCmd()
	subs := map[string]bool{}
	for _, c := range gp.Commands() {
		subs[c.Name()] = true
	}
	for _, name := range []string{"list", "create", "delete", "connect"} {
		if !subs[name] {
			t.Errorf("missing git-provider subcommand: %s", name)
		}
	}
}

func TestDeviceCodeRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/auth/git/github/device"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(DeviceCodeResponse{
			DeviceCode:      "dc123",
			UserCode:        "ABCD-1234",
			VerificationURI: "https://github.com/login/device",
			ExpiresIn:       900,
			Interval:        5,
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	resp, err := c.RequestDeviceCode("github")
	if err != nil {
		t.Fatal(err)
	}
	if resp.UserCode != "ABCD-1234" {
		t.Errorf("unexpected user code: %q", resp.UserCode)
	}
}

func TestDeviceCodePoll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/auth/git/github/device/poll"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		_ = json.NewEncoder(w).Encode(DevicePollResponse{Status: "complete"})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	resp, err := c.PollDeviceCode("github", "dc123")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != "complete" {
		t.Errorf("unexpected status: %q", resp.Status)
	}
}
