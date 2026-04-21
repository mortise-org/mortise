package api

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/authz"
	"github.com/MC-Meesh/mortise/internal/git"
)

// ListRepos returns the repositories visible to the authenticated user
// for the given git provider.
//
// GET /api/repos?provider=github
func (s *Server) ListRepos(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, authz.Resource{Kind: "gitprovider"}, authz.ActionRead) {
		return
	}
	api, err := s.resolveGitAPI(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{err.Error()})
		return
	}

	repos, err := api.ListRepos(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorResponse{fmt.Sprintf("list repos: %s", err)})
		return
	}

	writeJSON(w, http.StatusOK, repos)
}

// ListBranches returns the branches for a repository.
//
// GET /api/repos/{owner}/{repo}/branches?provider=github
func (s *Server) ListBranches(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, authz.Resource{Kind: "gitprovider"}, authz.ActionRead) {
		return
	}
	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")
	fullRepo := owner + "/" + repo

	api, err := s.resolveGitAPI(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{err.Error()})
		return
	}

	branches, err := api.ListBranches(r.Context(), fullRepo)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorResponse{fmt.Sprintf("list branches: %s", err)})
		return
	}

	writeJSON(w, http.StatusOK, branches)
}

// GetRepoTree returns the immediate children of a path in a repository.
//
// GET /api/repos/{owner}/{repo}/tree?provider=github&branch=Y&path=Z
func (s *Server) GetRepoTree(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, authz.Resource{Kind: "gitprovider"}, authz.ActionRead) {
		return
	}
	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")
	branch := r.URL.Query().Get("branch")
	if branch == "" {
		branch = "main"
	}
	path := r.URL.Query().Get("path")

	api, err := s.resolveGitAPI(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{err.Error()})
		return
	}

	entries, err := api.ListTree(r.Context(), owner, repo, branch, path)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorResponse{fmt.Sprintf("list tree: %s", err)})
		return
	}

	writeJSON(w, http.StatusOK, entries)
}

// resolveGitAPI determines the GitAPI to use based on the ?provider= query
// param and the calling user's per-user token for that provider.
func (s *Server) resolveGitAPI(r *http.Request) (git.GitAPI, error) {
	providerName := r.URL.Query().Get("provider")
	if providerName == "" {
		return nil, fmt.Errorf("provider query parameter is required")
	}

	var gp mortisev1alpha1.GitProvider
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: providerName}, &gp); err != nil {
		return nil, fmt.Errorf("git provider %q not found: %w", providerName, err)
	}

	principal := PrincipalFromContext(r.Context())
	if principal == nil {
		return nil, fmt.Errorf("authentication required")
	}

	token, err := git.ResolveGitToken(r.Context(), s.client, providerName, principal.Email)
	if err != nil {
		return nil, fmt.Errorf("git not connected for provider %q — connect from your profile", providerName)
	}

	api, err := git.NewGitAPIFromProvider(&gp, token, "")
	if err != nil {
		return nil, fmt.Errorf("create git api for %q: %w", providerName, err)
	}

	return api, nil
}
