package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/git"
)

// ListRepos returns the repositories visible to the authenticated user.
// For GitHub, uses the calling user's per-user token. For other providers,
// accepts an optional ?provider= query param.
//
// GET /api/repos
func (s *Server) ListRepos(w http.ResponseWriter, r *http.Request) {
	api, err := s.resolveGitAPI(r)
	if err != nil {
		status := http.StatusBadRequest
		if err == errGitHubNotConnected {
			status = http.StatusNotFound
		}
		writeJSON(w, status, errorResponse{err.Error()})
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
// GET /api/repos/{owner}/{repo}/branches
func (s *Server) ListBranches(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")
	fullRepo := owner + "/" + repo

	api, err := s.resolveGitAPI(r)
	if err != nil {
		status := http.StatusBadRequest
		if err == errGitHubNotConnected {
			status = http.StatusNotFound
		}
		writeJSON(w, status, errorResponse{err.Error()})
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
// GET /api/repos/{owner}/{repo}/tree?branch=Y&path=Z
func (s *Server) GetRepoTree(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")
	branch := r.URL.Query().Get("branch")
	if branch == "" {
		branch = "main"
	}
	path := r.URL.Query().Get("path")

	api, err := s.resolveGitAPI(r)
	if err != nil {
		status := http.StatusBadRequest
		if err == errGitHubNotConnected {
			status = http.StatusNotFound
		}
		writeJSON(w, status, errorResponse{err.Error()})
		return
	}

	entries, err := api.ListTree(r.Context(), owner, repo, branch, path)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorResponse{fmt.Sprintf("list tree: %s", err)})
		return
	}

	writeJSON(w, http.StatusOK, entries)
}

var errGitHubNotConnected = fmt.Errorf("GitHub not connected. Connect from your profile.")

// resolveGitAPI determines the GitAPI to use. If ?provider= is given, uses that
// GitProvider CRD (for GitLab/Gitea). Otherwise, uses the calling user's
// per-user GitHub token.
func (s *Server) resolveGitAPI(r *http.Request) (git.GitAPI, error) {
	providerName := r.URL.Query().Get("provider")
	if providerName != "" {
		return s.gitAPIForProvider(r.Context(), providerName)
	}

	// Default: use calling user's GitHub token.
	principal := PrincipalFromContext(r.Context())
	if principal == nil {
		return nil, fmt.Errorf("authentication required")
	}

	token, err := ResolveUserGitHubToken(r.Context(), s.client, principal.Email)
	if err != nil {
		return nil, errGitHubNotConnected
	}

	return git.NewGitHubAPI("https://github.com", token, "")
}

// gitAPIForProvider resolves a GitProvider CRD + stored credentials and
// constructs the appropriate GitAPI implementation.
func (s *Server) gitAPIForProvider(ctx context.Context, providerName string) (git.GitAPI, error) {
	var gp mortisev1alpha1.GitProvider
	if err := s.client.Get(ctx, types.NamespacedName{Name: providerName}, &gp); err != nil {
		return nil, fmt.Errorf("get git provider %q: %w", providerName, err)
	}

	token, err := git.ResolveProviderToken(ctx, s.client, &gp)
	if err != nil {
		return nil, fmt.Errorf("resolve token for %q: %w", providerName, err)
	}

	api, err := git.NewGitAPIFromProvider(&gp, token, "")
	if err != nil {
		return nil, fmt.Errorf("create git api for %q: %w", providerName, err)
	}

	return api, nil
}
