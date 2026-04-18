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

// ListRepos returns the repositories visible to the authenticated user for a
// given git provider.
//
// GET /api/repos?provider={providerName}
func (s *Server) ListRepos(w http.ResponseWriter, r *http.Request) {
	providerName := r.URL.Query().Get("provider")
	if providerName == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"provider query param is required"})
		return
	}

	api, err := s.gitAPIForProvider(r.Context(), providerName)
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

// ListBranches returns the branches for a repository on a given git provider.
//
// GET /api/repos/{owner}/{repo}/branches?provider={providerName}
func (s *Server) ListBranches(w http.ResponseWriter, r *http.Request) {
	providerName := r.URL.Query().Get("provider")
	if providerName == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"provider query param is required"})
		return
	}

	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")
	fullRepo := owner + "/" + repo

	api, err := s.gitAPIForProvider(r.Context(), providerName)
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
// GET /api/repos/{owner}/{repo}/tree?provider=X&branch=Y&path=Z
func (s *Server) GetRepoTree(w http.ResponseWriter, r *http.Request) {
	providerName := r.URL.Query().Get("provider")
	if providerName == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"provider query param is required"})
		return
	}

	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")
	branch := r.URL.Query().Get("branch")
	if branch == "" {
		branch = "main"
	}
	path := r.URL.Query().Get("path")

	api, err := s.gitAPIForProvider(r.Context(), providerName)
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

// gitAPIForProvider resolves a GitProvider CRD + stored credentials and
// constructs the appropriate GitAPI implementation. For github-app mode
// providers, it reads the private key from the credentials secret; for
// oauth mode it reads the stored OAuth token.
func (s *Server) gitAPIForProvider(ctx context.Context, providerName string) (git.GitAPI, error) {
	var gp mortisev1alpha1.GitProvider
	if err := s.client.Get(ctx, types.NamespacedName{Name: providerName}, &gp); err != nil {
		return nil, fmt.Errorf("get git provider %q: %w", providerName, err)
	}

	if gp.Spec.Mode == "github-app" && gp.Spec.GitHubApp != nil {
		privateKey, webhookSecret, err := git.ResolveGitHubAppCredentials(ctx, s.client, &gp)
		if err != nil {
			return nil, fmt.Errorf("resolve github app credentials for %q: %w", providerName, err)
		}
		api, err := git.NewGitHubAppAPIFromProvider(&gp, privateKey, webhookSecret)
		if err != nil {
			return nil, fmt.Errorf("create github app api for %q: %w", providerName, err)
		}
		return api, nil
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
