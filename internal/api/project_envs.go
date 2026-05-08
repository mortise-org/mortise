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

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/envstore"
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
	Restricted   bool      `json:"restricted,omitempty"`
}

type createProjectEnvRequest struct {
	Name         string `json:"name"`
	DisplayOrder int    `json:"displayOrder,omitempty"`
}

// patchProjectEnvRequest is the JSON body for PATCH .../environments/{name}.
// All fields are optional — omitting a field leaves the existing value in place.
type patchProjectEnvRequest struct {
	Name         *string `json:"name,omitempty"`
	DisplayOrder *int    `json:"displayOrder,omitempty"`
	Restricted   *bool   `json:"restricted,omitempty"`
}

// ListProjectEnvironments returns the project's ordered env list with an
// aggregated health dot for each one.
//
// GET /api/projects/{project}/environments
//
// @Summary List project environments
// @Description Returns the project's ordered environment list with aggregated health status
// @Tags environments
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Success 200 {array} projectEnvResponse
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/environments [get]
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
		writeError(w, r, err)
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
			Restricted:   env.Restricted,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// CreateProjectEnvironment appends a new env to spec.environments. Admin-only.
//
// POST /api/projects/{project}/environments  { "name": "staging" }
//
// @Summary Create a project environment
// @Description Appends a new environment to the project. Admin-only.
// @Tags environments
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param body body createProjectEnvRequest true "Environment name and display order"
// @Success 201 {object} projectEnvResponse
// @Failure 400 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Failure 409 {object} errorResponse
// @Router /projects/{project}/environments [post]
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
		writeError(w, r, err)
		return
	}

	s.recordActivity(r, projectName, "create", "environment", req.Name, "Created project environment "+req.Name, "")

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
//
// @Summary Update a project environment
// @Description Edits the display order and/or renames an environment. When displayOrder changes, the displaced environment is automatically swapped. Renames cascade to App overrides.
// @Tags environments
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param name path string true "Environment name"
// @Param body body patchProjectEnvRequest true "Fields to update"
// @Success 200 {object} projectEnvResponse
// @Failure 400 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Failure 409 {object} errorResponse
// @Router /projects/{project}/environments/{name} [patch]
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
			writeError(w, r, err)
			return
		}
		project.Spec.Environments[idx].Name = *req.Name
	}
	if req.DisplayOrder != nil {
		oldOrder := project.Spec.Environments[idx].DisplayOrder
		newOrder := *req.DisplayOrder
		if oldOrder != newOrder {
			for i := range project.Spec.Environments {
				if i != idx && project.Spec.Environments[i].DisplayOrder == newOrder {
					project.Spec.Environments[i].DisplayOrder = oldOrder
					break
				}
			}
			project.Spec.Environments[idx].DisplayOrder = newOrder
		}
	}
	if req.Restricted != nil {
		project.Spec.Environments[idx].Restricted = *req.Restricted
	}

	if err := s.client.Update(r.Context(), project); err != nil {
		writeError(w, r, err)
		return
	}

	updated := project.Spec.Environments[idx]
	msg := "Updated project environment " + updated.Name
	if req.Name != nil && *req.Name != envName {
		msg = "Renamed project environment " + envName + " to " + updated.Name
	}
	s.recordActivity(r, projectName, "update", "environment", updated.Name, msg, "")
	writeJSON(w, http.StatusOK, projectEnvResponse{
		Name:         updated.Name,
		DisplayOrder: updated.DisplayOrder,
		Health:       EnvHealthUnknown,
		Restricted:   updated.Restricted,
	})
}

// DeleteProjectEnvironment removes an env from spec.environments. The
// admission webhook rejects the call if any App still carries an override for
// the env; the API surfaces that 403 verbatim.
//
// DELETE /api/projects/{project}/environments/{name}
//
// @Summary Delete a project environment
// @Description Removes an environment from the project. Fails if apps still have overrides for it.
// @Tags environments
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param name path string true "Environment name"
// @Success 200 {object} map[string]string "Deletion confirmation"
// @Failure 400 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Failure 409 {object} errorResponse
// @Router /projects/{project}/environments/{name} [delete]
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
		writeError(w, r, err)
		return
	}

	s.recordActivity(r, projectName, "delete", "environment", envName, "Deleted project environment "+envName, "")

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": envName})
}

type cloneProjectEnvRequest struct {
	Name         string `json:"name"`
	DisplayOrder int    `json:"displayOrder,omitempty"`
}

// CloneProjectEnvironment creates a new environment by copying the full
// configuration from an existing source environment. For every App in the
// project, CRD-level overrides (replicas, resources, probes, bindings,
// annotations) and Secret-level env vars (set via the UI/API) are merged
// into the new env entry. Binding-sourced vars are excluded because the
// controller re-resolves them in the new namespace.
//
// The operation is retry-safe: if a previous call partially completed
// (project env created but app overrides not fully applied), a repeat
// call skips the project update and continues cloning app overrides,
// returning 200 instead of 201.
//
// POST /api/projects/{project}/environments/{source}/clone  { "name": "staging" }
//
// @Summary Clone a project environment
// @Description Creates a new environment pre-populated with the source's config (CRD overrides + Secret-level env vars) for every app. Retry-safe: returns 200 if the target already exists.
// @Tags environments
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param source path string true "Source environment name"
// @Param body body cloneProjectEnvRequest true "Target environment name and display order"
// @Success 200 {object} projectEnvResponse "Retry: target env already existed"
// @Success 201 {object} projectEnvResponse "Created"
// @Failure 400 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/environments/{source}/clone [post]
func (s *Server) CloneProjectEnvironment(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "project", Project: projectName}, authz.ActionCreate) {
		return
	}
	project, ok := s.getProject(w, r)
	if !ok {
		return
	}
	sourceName := chi.URLParam(r, "source")

	if indexOfEnv(project, sourceName) < 0 {
		writeJSON(w, http.StatusNotFound, errorResponse{fmt.Sprintf("source environment %q not found on project %q", sourceName, project.Name)})
		return
	}

	var req cloneProjectEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if msg := validateDNSLabel("name", req.Name, maxProjectEnvNameLen); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{msg})
		return
	}

	// Retry-safe: if the target env already exists on the project (partial
	// failure added the env but didn't finish cloning app overrides), skip
	// the project update and continue with per-app cloning.
	targetExists := false
	for _, existing := range project.Spec.Environments {
		if existing.Name == req.Name {
			targetExists = true
			break
		}
	}

	if !targetExists {
		project.Spec.Environments = append(project.Spec.Environments, mortisev1alpha1.ProjectEnvironment{
			Name:         req.Name,
			DisplayOrder: req.DisplayOrder,
		})
		if err := s.client.Update(r.Context(), project); err != nil {
			writeError(w, r, err)
			return
		}
	}

	// Clone env overrides (CRD + Secret-level) from source to target for every App.
	if err := s.cloneAppOverrides(r.Context(), projectName, projectNs(project), sourceName, req.Name); err != nil {
		writeError(w, r, err)
		return
	}

	s.recordActivity(r, projectName, "create", "environment", req.Name,
		fmt.Sprintf("Cloned environment %s from %s", req.Name, sourceName), "")

	status := http.StatusCreated
	if targetExists {
		status = http.StatusOK
	}
	writeJSON(w, status, projectEnvResponse{
		Name:         req.Name,
		DisplayOrder: req.DisplayOrder,
		Health:       EnvHealthUnknown,
	})
}

// cloneAppOverrides copies the source environment's overrides (env vars,
// bindings, resources, replicas, probes, schedule, annotations) to a new
// environment entry on every App in the project. It also reads Secret-level
// env vars (set via the UI/API) and merges them into the cloned CRD entry
// so users who set env vars only through the UI don't get an empty clone.
// Binding-sourced vars are excluded — the controller re-resolves them.
func (s *Server) cloneAppOverrides(ctx context.Context, projectName, ns, sourceName, targetName string) error {
	var apps mortisev1alpha1.AppList
	if err := s.client.List(ctx, &apps, client.InNamespace(ns)); err != nil {
		return err
	}
	sourceEnvNs := constants.EnvNamespace(projectName, sourceName)
	store := &envstore.Store{Client: s.client}

	for i := range apps.Items {
		appName := apps.Items[i].Name
		if err := s.cloneEnvToApp(ctx, ns, appName, sourceName, targetName, sourceEnvNs, store); err != nil {
			return err
		}
	}
	return nil
}

const maxConflictRetries = 5

func (s *Server) cloneEnvToApp(ctx context.Context, ns, appName, sourceName, targetName, sourceEnvNs string, store *envstore.Store) error {
	secretVars, err := store.Get(ctx, sourceEnvNs, appName)
	if err != nil {
		return fmt.Errorf("read source env vars for app %q: %w", appName, err)
	}

	for attempt := range maxConflictRetries {
		var app mortisev1alpha1.App
		if err := s.client.Get(ctx, types.NamespacedName{Namespace: ns, Name: appName}, &app); err != nil {
			return fmt.Errorf("get app %q: %w", appName, err)
		}

		hasTarget := false
		for j := range app.Spec.Environments {
			if app.Spec.Environments[j].Name == targetName {
				hasTarget = true
				break
			}
		}
		if hasTarget {
			return nil
		}

		var sourceEnv *mortisev1alpha1.Environment
		for j := range app.Spec.Environments {
			if app.Spec.Environments[j].Name == sourceName {
				sourceEnv = &app.Spec.Environments[j]
				break
			}
		}

		envMap := make(map[string]mortisev1alpha1.EnvVar)
		for _, ev := range secretVars {
			if ev.Source == "binding" {
				continue
			}
			envMap[ev.Name] = mortisev1alpha1.EnvVar{Name: ev.Name, Value: ev.Value}
		}
		if sourceEnv != nil {
			for _, ev := range sourceEnv.Env {
				envMap[ev.Name] = ev
			}
		}

		cloned := mortisev1alpha1.Environment{Name: targetName}
		if sourceEnv != nil {
			cloned.Replicas = sourceEnv.Replicas
			cloned.Resources = sourceEnv.Resources
			cloned.LivenessProbe = sourceEnv.LivenessProbe
			cloned.ReadinessProbe = sourceEnv.ReadinessProbe
			cloned.StartupProbe = sourceEnv.StartupProbe
			cloned.Schedule = sourceEnv.Schedule
			cloned.ConcurrencyPolicy = sourceEnv.ConcurrencyPolicy
			if len(sourceEnv.Bindings) > 0 {
				cloned.Bindings = make([]mortisev1alpha1.Binding, len(sourceEnv.Bindings))
				copy(cloned.Bindings, sourceEnv.Bindings)
			}
			if len(sourceEnv.Annotations) > 0 {
				cloned.Annotations = make(map[string]string, len(sourceEnv.Annotations))
				for k, v := range sourceEnv.Annotations {
					cloned.Annotations[k] = v
				}
			}
			if len(sourceEnv.BuildArgs) > 0 {
				cloned.BuildArgs = make(map[string]string, len(sourceEnv.BuildArgs))
				for k, v := range sourceEnv.BuildArgs {
					cloned.BuildArgs[k] = v
				}
			}
		}
		if len(envMap) > 0 {
			cloned.Env = make([]mortisev1alpha1.EnvVar, 0, len(envMap))
			for _, ev := range envMap {
				cloned.Env = append(cloned.Env, ev)
			}
			sort.Slice(cloned.Env, func(a, b int) bool { return cloned.Env[a].Name < cloned.Env[b].Name })
		}

		app.Spec.Environments = append(app.Spec.Environments, cloned)
		updateErr := s.client.Update(ctx, &app)
		if updateErr == nil {
			return nil
		}
		if !errors.IsConflict(updateErr) || attempt == maxConflictRetries-1 {
			return fmt.Errorf("clone env overrides for app %q: %w", appName, updateErr)
		}
	}
	return nil
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
		writeError(w, r, err)
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

// phaseForEnv returns the per-env phase when available, falling back to the
// app-wide aggregate phase for older status entries that predate per-env tracking.
func phaseForEnv(app *mortisev1alpha1.App, envName string) mortisev1alpha1.AppPhase {
	for _, es := range app.Status.Environments {
		if es.Name != envName {
			continue
		}
		if es.Phase != "" {
			return es.Phase
		}
		return app.Status.Phase
	}
	return app.Status.Phase
}
