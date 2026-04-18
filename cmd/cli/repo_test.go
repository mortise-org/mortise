package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRepoList_QueriesCorrectPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/repos"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if got := r.URL.Query().Get("provider"); got != "github-main" {
			t.Errorf("expected provider=github-main, got %q", got)
		}
		_ = json.NewEncoder(w).Encode([]Repository{
			{FullName: "org/repo", Name: "repo", DefaultBranch: "main"},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	repos, err := c.ListRepos("github-main")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].FullName != "org/repo" {
		t.Errorf("unexpected repos: %+v", repos)
	}
}

func TestRepoBranches_QueriesCorrectPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/repos/myorg/myrepo/branches"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if got := r.URL.Query().Get("provider"); got != "github-main" {
			t.Errorf("expected provider=github-main, got %q", got)
		}
		_ = json.NewEncoder(w).Encode([]Branch{
			{Name: "main", Default: true},
			{Name: "develop", Default: false},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	branches, err := c.ListBranches("myorg", "myrepo", "github-main")
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 2 {
		t.Errorf("expected 2 branches, got %d", len(branches))
	}
	if !branches[0].Default {
		t.Errorf("expected first branch to be default")
	}
}

func TestRepoSubcommands(t *testing.T) {
	repo := newRepoCmd()
	subs := map[string]bool{}
	for _, c := range repo.Commands() {
		subs[c.Name()] = true
	}
	for _, name := range []string{"list", "branches"} {
		if !subs[name] {
			t.Errorf("missing repo subcommand: %s", name)
		}
	}
}
