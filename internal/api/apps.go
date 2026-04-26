package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
)

// normalizeRepoURL expands short-form "owner/repo" strings to a full git URL.
// If the URL already starts with http:// or https:// it is returned unchanged.
// Otherwise it is treated as owner/repo shorthand: the host is resolved from
// the GitProvider named by providerRef (or the first GitProvider in the cluster
// when providerRef is empty). A ".git" suffix is appended if absent.
// Falls back to https://github.com when no GitProvider can be found.
func (s *Server) normalizeRepoURL(ctx context.Context, ns, providerRef, repo string) string {
	if strings.HasPrefix(repo, "http://") || strings.HasPrefix(repo, "https://") {
		return repo
	}

	host := "https://github.com"

	if providerRef != "" {
		var gp mortisev1alpha1.GitProvider
		if err := s.client.Get(ctx, types.NamespacedName{Name: providerRef}, &gp); err == nil {
			host = strings.TrimRight(gp.Spec.Host, "/")
		}
	} else {
		var list mortisev1alpha1.GitProviderList
		if err := s.client.List(ctx, &list); err == nil && len(list.Items) > 0 {
			host = strings.TrimRight(list.Items[0].Spec.Host, "/")
		}
	}

	url := host + "/" + repo
	if !strings.HasSuffix(url, ".git") {
		url += ".git"
	}
	return url
}

// maxAppNameLen caps app names. App names are suffixed with "-{env}" in
// Deployment names; the longest env suffix is ~10 chars, and k8s names max
// at 63, so 53 keeps the composed name safe.
const maxAppNameLen = 53

// maxEnvNameLen caps environment names. These appear as suffixes in
// Deployment names (e.g. "myapp-production").
const maxEnvNameLen = 63

// createAppRequest is the JSON body for POST /api/projects/{project}/apps.
// Namespace is NOT caller-specified — it's always the project's namespace.
type createAppRequest struct {
	Name string                  `json:"name"`
	Spec mortisev1alpha1.AppSpec `json:"spec"`
}

// @Summary Create an app
// @Description Creates a new App within a project
// @Tags apps
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param body body createAppRequest true "App details"
// @Success 201 {object} v1alpha1.App
// @Failure 400 {object} errorResponse
// @Failure 401 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps [post]
func (s *Server) CreateApp(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "app", Namespace: ns, Project: projectName}, authz.ActionCreate) {
		return
	}

	var req createAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if msg := validateDNSLabel("name", req.Name, maxAppNameLen); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{msg})
		return
	}
	for i, env := range req.Spec.Environments {
		if msg := validateDNSLabel(fmt.Sprintf("environments[%d].name", i), env.Name, maxEnvNameLen); msg != "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{msg})
			return
		}
	}

	// Normalize short-form repo URLs (owner/repo → full https URL) before
	// storing on the CR. go-git requires a full URL.
	if req.Spec.Source.Type == mortisev1alpha1.SourceTypeGit {
		req.Spec.Source.Repo = s.normalizeRepoURL(r.Context(), ns, req.Spec.Source.ProviderRef, req.Spec.Source.Repo)
	}

	// Stamp which user created this app so the controller can resolve
	// their per-user GitHub token for git-source builds.
	annotations := map[string]string{}
	if p := PrincipalFromContext(r.Context()); p != nil {
		annotations["mortise.dev/created-by"] = p.Email
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:        req.Name,
			Namespace:   ns,
			Annotations: annotations,
		},
		Spec: req.Spec,
	}

	if err := s.client.Create(r.Context(), app); err != nil {
		writeError(w, err)
		return
	}

	s.recordActivity(r, projectName, "create", "app", app.Name, "Created app "+app.Name, "")

	writeJSON(w, http.StatusCreated, app)
}

// @Summary List apps
// @Description Returns all Apps within a project
// @Tags apps
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Success 200 {array} v1alpha1.App
// @Failure 401 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps [get]
func (s *Server) ListApps(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "app", Namespace: ns, Project: projectName}, authz.ActionRead) {
		return
	}

	var list mortisev1alpha1.AppList
	if err := s.client.List(r.Context(), &list, client.InNamespace(ns)); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, list.Items)
}

// @Summary Get an app
// @Description Returns a single App by name within a project
// @Tags apps
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Success 200 {object} v1alpha1.App
// @Failure 401 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app} [get]
func (s *Server) GetApp(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "app", Namespace: ns, Project: projectName}, authz.ActionRead) {
		return
	}
	name := chi.URLParam(r, "app")

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, &app)
}

// @Summary Update an app
// @Description Updates an existing App's spec
// @Tags apps
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Param body body v1alpha1.AppSpec true "Updated app spec"
// @Success 200 {object} v1alpha1.App
// @Failure 400 {object} errorResponse
// @Failure 401 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app} [put]
func (s *Server) UpdateApp(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "app", Namespace: ns, Project: projectName}, authz.ActionUpdate) {
		return
	}
	name := chi.URLParam(r, "app")

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	var spec mortisev1alpha1.AppSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}

	// Normalize short-form repo URLs on update too.
	if spec.Source.Type == mortisev1alpha1.SourceTypeGit {
		spec.Source.Repo = s.normalizeRepoURL(r.Context(), ns, spec.Source.ProviderRef, spec.Source.Repo)
	}

	app.Spec = spec
	if err := s.client.Update(r.Context(), &app); err != nil {
		writeError(w, err)
		return
	}

	s.recordActivity(r, projectName, "update", "app", app.Name, "Updated app "+app.Name, "")

	writeJSON(w, http.StatusOK, &app)
}

// @Summary Delete an app
// @Description Deletes an App from a project
// @Tags apps
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Success 200 {object} map[string]string
// @Failure 401 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app} [delete]
func (s *Server) DeleteApp(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "app", Namespace: ns, Project: projectName}, authz.ActionDelete) {
		return
	}
	name := chi.URLParam(r, "app")

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	if err := s.client.Delete(r.Context(), &app); err != nil {
		writeError(w, err)
		return
	}

	s.recordActivity(r, projectName, "delete", "app", app.Name, "Deleted app "+app.Name, "")

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// writeError maps k8s API errors to HTTP status codes.
func writeError(w http.ResponseWriter, err error) {
	if errors.IsNotFound(err) {
		writeJSON(w, http.StatusNotFound, errorResponse{err.Error()})
		return
	}
	if errors.IsAlreadyExists(err) {
		writeJSON(w, http.StatusConflict, errorResponse{err.Error()})
		return
	}
	if errors.IsInvalid(err) {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{err.Error()})
		return
	}
	writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
}
