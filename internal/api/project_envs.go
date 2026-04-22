package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/authz"
)

// maxProjectEnvNameLen caps project env names. Environment names are used as
// suffixes in Deployment names (e.g. "myapp-production"), so they must fit
// inside k8s' 63-char label cap.
const maxProjectEnvNameLen = 63

// EnvHealth reports the aggregated rollout state of a project environment
// across every App that participates in it. The UI renders one status dot per
// env on the navbar from this value.
type EnvHealth string

const (
	EnvHealthHealthy EnvHealth = "healthy"
	EnvHealthWarning EnvHealth = "warning"
	EnvHealthDanger  EnvHealth = "danger"
	EnvHealthUnknown EnvHealth = "unknown"
)

// projectEnvResponse mirrors ProjectEnvironment plus a UI-facing health roll-up
// across every App participating in that env.
type projectEnvResponse struct {
	Name         string    `json:"name"`
	DisplayOrder int       `json:"displayOrder"`
	Health       EnvHealth `json:"health"`
}

type createProjectEnvRequest struct {
	Name         string `json:"name"`
	DisplayOrder int    `json:"displayOrder,omitempty"`
}

// patchProjectEnvRequest is the JSON body for PATCH .../environments/{name}.
// Both fields are optional — omitting a field leaves the existing value in place.
type patchProjectEnvRequest struct {
	Name         *string `json:"name,omitempty"`
	DisplayOrder *int    `json:"displayOrder,omitempty"`
}

// ListProjectEnvironments returns the project's ordered env list with an
// aggregated health dot for each one.
//
// GET /api/projects/{project}/environments
func (s *Server) ListProjectEnvironments(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "project", Project: projectName}, authz.ActionRead) {
		return
	}
	project, ok := s.getProject(w, r)
	if !ok {
		return
	}

	ns := projectNs(project)
	var apps mortisev1alpha1.AppList
	if err := s.client.List(r.Context(), &apps, client.InNamespace(ns)); err != nil {
		writeError(w, err)
		return
	}

	envs := make([]mortisev1alpha1.ProjectEnvironment, len(project.Spec.Environments))
	copy(envs, project.Spec.Environments)
	sort.SliceStable(envs, func(i, j int) bool { return envs[i].DisplayOrder < envs[j].DisplayOrder })

	resp := make([]projectEnvResponse, 0, len(envs))
	for _, env := range envs {
		resp = append(resp, projectEnvResponse{
			Name:         env.Name,
			DisplayOrder: env.DisplayOrder,
			Health:       aggregateEnvHealth(env.Name, apps.Items),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// CreateProjectEnvironment appends a new env to spec.environments. Admin-only.
//
// POST /api/projects/{project}/environments  { "name": "staging" }
func (s *Server) CreateProjectEnvironment(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "project", Project: projectName}, authz.ActionCreate) {
		return
	}
	project, ok := s.getProject(w, r)
	if !ok {
		return
	}

	var req createProjectEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if msg := validateDNSLabel("name", req.Name, maxProjectEnvNameLen); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{msg})
		return
	}
	for _, existing := range project.Spec.Environments {
		if existing.Name == req.Name {
			writeJSON(w, http.StatusConflict, errorResponse{fmt.Sprintf("environment %q already exists on project %q", req.Name, project.Name)})
			return
		}
	}

	project.Spec.Environments = append(project.Spec.Environments, mortisev1alpha1.ProjectEnvironment{
		Name:         req.Name,
		DisplayOrder: req.DisplayOrder,
	})
	if err := s.client.Update(r.Context(), project); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, projectEnvResponse{
		Name:         req.Name,
		DisplayOrder: req.DisplayOrder,
		Health:       EnvHealthUnknown,
	})
}

// UpdateProjectEnvironment edits the display order and/or renames an env.
// Renaming cascades to App overrides in the project namespace so the
// admission webhook's "override names must exist on project" rule stays
// satisfied after the update lands.
//
// PATCH /api/projects/{project}/environments/{name}  { "name": "stage", "displayOrder": 2 }
func (s *Server) UpdateProjectEnvironment(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "project", Project: projectName}, authz.ActionUpdate) {
		return
	}
	project, ok := s.getProject(w, r)
	if !ok {
		return
	}
	envName := chi.URLParam(r, "name")

	var req patchProjectEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}

	idx := indexOfEnv(project, envName)
	if idx < 0 {
		writeJSON(w, http.StatusNotFound, errorResponse{fmt.Sprintf("environment %q not found on project %q", envName, project.Name)})
		return
	}

	if req.Name != nil && *req.Name != envName {
		if msg := validateDNSLabel("name", *req.Name, maxProjectEnvNameLen); msg != "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{msg})
			return
		}
		if indexOfEnv(project, *req.Name) >= 0 {
			writeJSON(w, http.StatusConflict, errorResponse{fmt.Sprintf("environment %q already exists on project %q", *req.Name, project.Name)})
			return
		}
		// Rename App overrides first so the admission webhook doesn't reject
		// the project update when its post-state includes an env name that
		// disappeared from overrides.
		if err := s.renameAppOverrides(r.Context(), projectNs(project), envName, *req.Name); err != nil {
			writeError(w, err)
			return
		}
		project.Spec.Environments[idx].Name = *req.Name
	}
	if req.DisplayOrder != nil {
		project.Spec.Environments[idx].DisplayOrder = *req.DisplayOrder
	}

	if err := s.client.Update(r.Context(), project); err != nil {
		writeError(w, err)
		return
	}

	updated := project.Spec.Environments[idx]
	writeJSON(w, http.StatusOK, projectEnvResponse{
		Name:         updated.Name,
		DisplayOrder: updated.DisplayOrder,
		Health:       EnvHealthUnknown,
	})
}

// DeleteProjectEnvironment removes an env from spec.environments. The
// admission webhook rejects the call if any App still carries an override for
// the env; the API surfaces that 403 verbatim.
//
// DELETE /api/projects/{project}/environments/{name}
func (s *Server) DeleteProjectEnvironment(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "project", Project: projectName}, authz.ActionDelete) {
		return
	}
	project, ok := s.getProject(w, r)
	if !ok {
		return
	}
	envName := chi.URLParam(r, "name")

	idx := indexOfEnv(project, envName)
	if idx < 0 {
		writeJSON(w, http.StatusNotFound, errorResponse{fmt.Sprintf("environment %q not found on project %q", envName, project.Name)})
		return
	}
	if len(project.Spec.Environments) == 1 {
		writeJSON(w, http.StatusBadRequest, errorResponse{"cannot delete the last environment on a project — delete the project instead"})
		return
	}

	project.Spec.Environments = append(project.Spec.Environments[:idx], project.Spec.Environments[idx+1:]...)
	if err := s.client.Update(r.Context(), project); err != nil {
		if errors.IsForbidden(err) {
			writeJSON(w, http.StatusConflict, errorResponse{err.Error()})
			return
		}
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": envName})
}

// getProject is like resolveProject but returns the full Project pointer so
// callers can mutate and update the CRD.
func (s *Server) getProject(w http.ResponseWriter, r *http.Request) (*mortisev1alpha1.Project, bool) {
	projectName := chi.URLParam(r, "project")
	if projectName == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"project is required"})
		return nil, false
	}

	var project mortisev1alpha1.Project
	err := s.client.Get(r.Context(), types.NamespacedName{Name: projectName}, &project)
	if errors.IsNotFound(err) {
		writeJSON(w, http.StatusNotFound, errorResponse{fmt.Sprintf("project %q not found", projectName)})
		return nil, false
	}
	if err != nil {
		writeError(w, err)
		return nil, false
	}
	return &project, true
}

// projectNs returns the control namespace for the project's Apps.
func projectNs(p *mortisev1alpha1.Project) string {
	if p.Status.Namespace != "" {
		return p.Status.Namespace
	}
	return projectNamespace(p.Name)
}

// indexOfEnv returns the index of the named environment in spec.environments,
// or -1 if absent.
func indexOfEnv(project *mortisev1alpha1.Project, name string) int {
	for i, env := range project.Spec.Environments {
		if env.Name == name {
			return i
		}
	}
	return -1
}

// renameAppOverrides walks every App in the project namespace and rewrites
// any spec.environments[].name == oldName to newName. Called before updating
// the Project so the admission webhook's "overrides must exist on project"
// invariant is preserved throughout the transition.
func (s *Server) renameAppOverrides(ctx context.Context, ns, oldName, newName string) error {
	var apps mortisev1alpha1.AppList
	if err := s.client.List(ctx, &apps, client.InNamespace(ns)); err != nil {
		return err
	}
	for i := range apps.Items {
		app := &apps.Items[i]
		changed := false
		for j := range app.Spec.Environments {
			if app.Spec.Environments[j].Name == oldName {
				app.Spec.Environments[j].Name = newName
				changed = true
			}
		}
		if !changed {
			continue
		}
		if err := s.client.Update(ctx, app); err != nil {
			return err
		}
	}
	return nil
}

// aggregateEnvHealth reduces per-app phase into a single navbar dot per env.
// Only apps that opt-in (no explicit `enabled: false` override) contribute.
func aggregateEnvHealth(envName string, apps []mortisev1alpha1.App) EnvHealth {
	var healthy, warn, danger, participating int
	for i := range apps {
		app := &apps[i]
		if !appParticipatesInEnv(app, envName) {
			continue
		}
		participating++
		switch phaseForEnv(app, envName) {
		case mortisev1alpha1.AppPhaseFailed, mortisev1alpha1.AppPhaseCrashLooping:
			danger++
		case mortisev1alpha1.AppPhaseBuilding, mortisev1alpha1.AppPhaseDeploying, mortisev1alpha1.AppPhasePending:
			warn++
		case mortisev1alpha1.AppPhaseReady:
			healthy++
		}
	}
	switch {
	case participating == 0:
		return EnvHealthUnknown
	case danger > 0:
		return EnvHealthDanger
	case warn > 0:
		return EnvHealthWarning
	case healthy == participating:
		return EnvHealthHealthy
	}
	return EnvHealthUnknown
}

// appParticipatesInEnv returns true unless the app has an explicit
// `enabled: false` override for this env.
func appParticipatesInEnv(app *mortisev1alpha1.App, envName string) bool {
	for _, env := range app.Spec.Environments {
		if env.Name != envName {
			continue
		}
		if env.Enabled != nil && !*env.Enabled {
			return false
		}
		return true
	}
	return true
}

// phaseForEnv picks the most relevant phase for this (app, env) pair. The App
// status doesn't yet track a phase per env, so we fall back to the app-wide
// phase — refined if/when the controller starts emitting per-env phases.
func phaseForEnv(app *mortisev1alpha1.App, envName string) mortisev1alpha1.AppPhase {
	for _, es := range app.Status.Environments {
		if es.Name != envName {
			continue
		}
		if app.Status.Phase == mortisev1alpha1.AppPhaseReady && es.ReadyReplicas == 0 {
			return mortisev1alpha1.AppPhaseDeploying
		}
		return app.Status.Phase
	}
	return app.Status.Phase
}
