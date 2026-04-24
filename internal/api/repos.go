package api

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/git"
)

// ListRepos returns the repositories visible to the authenticated user
// for the given git provider.
//
// GET /api/repos?provider=github
//
// @Summary List repositories
// @Description Return repositories visible to the authenticated user for the given git provider.
// @Tags repos
// @Produce json
// @Security BearerAuth
// @Param provider query string true "Git provider name (e.g. github)"
// @Success 200 {array} git.Repository
// @Failure 400 {object} errorResponse
// @Failure 502 {object} errorResponse
// @Router /repos [get]
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
//
// @Summary List branches for a repository
// @Description Return branches for a repository from the given git provider.
// @Tags repos
// @Produce json
// @Security BearerAuth
// @Param owner path string true "Repository owner"
// @Param repo path string true "Repository name"
// @Param provider query string true "Git provider name (e.g. github)"
// @Success 200 {array} git.Branch
// @Failure 400 {object} errorResponse
// @Failure 502 {object} errorResponse
// @Router /repos/{owner}/{repo}/branches [get]
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
//
// @Summary Get repository file tree
// @Description Return the immediate children of a path in a repository tree.
// @Tags repos
// @Produce json
// @Security BearerAuth
// @Param owner path string true "Repository owner"
// @Param repo path string true "Repository name"
// @Param provider query string true "Git provider name (e.g. github)"
// @Param branch query string false "Branch name (defaults to main)"
// @Param path query string false "Directory path within the repo"
// @Success 200 {array} git.TreeEntry
// @Failure 400 {object} errorResponse
// @Failure 502 {object} errorResponse
// @Router /repos/{owner}/{repo}/tree [get]
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
