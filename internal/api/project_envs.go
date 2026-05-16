package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
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

var previewProjectEnvNameRegex = regexp.MustCompile(`^pr-[0-9]+$`)

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
	Preview      bool      `json:"preview,omitempty"`
}

type createProjectEnvRequest struct {
	Name         string `json:"name"`
	DisplayOrder int    `json:"displayOrder,omitempty"`
}

type projectEnvExistsError struct {
	project string
	name    string
}

func (e *projectEnvExistsError) Error() string {
	return fmt.Sprintf("environment %q already exists on project %q", e.name, e.project)
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
			Preview:      env.Preview,
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
	if msg := validateProjectEnvName(req.Name); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{msg})
		return
	}
	for _, existing := range project.Spec.Environments {
		if existing.Name == req.Name {
			writeJSON(w, http.StatusConflict, errorResponse{fmt.Sprintf("environment %q already exists on project %q", req.Name, project.Name)})
			return
		}
	}
	if err := s.ensureProjectEnvNamespace(r.Context(), project, req.Name, false); err != nil {
		writeError(w, r, err)
		return
	}

	created, err := s.createProjectEnvironment(r.Context(), project.Name, req)
	if err != nil {
		var existsErr *projectEnvExistsError
		if errors.As(err, &existsErr) {
			writeJSON(w, http.StatusConflict, errorResponse{existsErr.Error()})
			return
		}
		_ = s.deleteProjectEnvNamespace(r.Context(), project, req.Name)
		writeError(w, r, err)
		return
	}
	if err := s.ensureProjectEnvNamespace(r.Context(), project, req.Name, true); err != nil {
		writeError(w, r, err)
		return
	}

	s.recordActivity(r, projectName, "create", "environment", req.Name, "Created project environment "+req.Name, "")

	writeJSON(w, http.StatusCreated, projectEnvResponse{
		Name:         created.Name,
		DisplayOrder: created.DisplayOrder,
		Health:       EnvHealthUnknown,
	})
}

func (s *Server) createProjectEnvironment(ctx context.Context, projectName string, req createProjectEnvRequest) (mortisev1alpha1.ProjectEnvironment, error) {
	created := mortisev1alpha1.ProjectEnvironment{
		Name:         req.Name,
		DisplayOrder: req.DisplayOrder,
	}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current mortisev1alpha1.Project
		if err := s.client.Get(ctx, types.NamespacedName{Name: projectName}, &current); err != nil {
			return err
		}
		for _, existing := range current.Spec.Environments {
			if existing.Name == req.Name {
				return &projectEnvExistsError{project: current.Name, name: req.Name}
			}
		}
		current.Spec.Environments = append(current.Spec.Environments, created)
		return s.client.Update(ctx, &current)
	}); err != nil {
		return mortisev1alpha1.ProjectEnvironment{}, err
	}
	return created, nil
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
		if msg := validateProjectEnvName(*req.Name); msg != "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{msg})
			return
		}
		if indexOfEnv(project, *req.Name) >= 0 {
			writeJSON(w, http.StatusConflict, errorResponse{fmt.Sprintf("environment %q already exists on project %q", *req.Name, project.Name)})
			return
		}
		if err := s.ensureProjectEnvNamespace(r.Context(), project, *req.Name, false); err != nil {
			writeError(w, r, err)
			return
		}
		if err := s.cloneCustomSecrets(r.Context(), constants.EnvNamespace(project.Name, envName), constants.EnvNamespace(project.Name, *req.Name)); err != nil {
			writeError(w, r, err)
			return
		}
		// Rename App overrides first so the admission webhook doesn't reject
		// the project update when its post-state includes an env name that
		// disappeared from overrides.
		if err := s.renameAppOverrides(r.Context(), projectName, projectNs(project), envName, *req.Name); err != nil {
			_ = s.deleteProjectEnvNamespace(r.Context(), project, *req.Name)
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

	updated, err := s.updateProjectEnvironment(r.Context(), project.Name, envName, req)
	if err != nil {
		if req.Name != nil && *req.Name != envName {
			_ = s.deleteProjectEnvNamespace(r.Context(), project, *req.Name)
		}
		writeError(w, r, err)
		return
	}
	if req.Name != nil && *req.Name != envName {
		if err := s.ensureProjectEnvNamespace(r.Context(), project, *req.Name, true); err != nil {
			writeError(w, r, err)
			return
		}
	}

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

func (s *Server) updateProjectEnvironment(ctx context.Context, projectName, envName string, req patchProjectEnvRequest) (mortisev1alpha1.ProjectEnvironment, error) {
	var updated mortisev1alpha1.ProjectEnvironment
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current mortisev1alpha1.Project
		if err := s.client.Get(ctx, types.NamespacedName{Name: projectName}, &current); err != nil {
			return err
		}

		idx := indexOfEnv(&current, envName)
		if idx < 0 {
			return apierrors.NewNotFound(
				schema.GroupResource{Group: mortisev1alpha1.GroupVersion.Group, Resource: "projectenvironments"},
				envName,
			)
		}

		if req.Name != nil && *req.Name != envName {
			if duplicateIdx := indexOfEnv(&current, *req.Name); duplicateIdx >= 0 && duplicateIdx != idx {
				return &projectEnvExistsError{project: current.Name, name: *req.Name}
			}
			current.Spec.Environments[idx].Name = *req.Name
		}
		if req.DisplayOrder != nil {
			oldOrder := current.Spec.Environments[idx].DisplayOrder
			newOrder := *req.DisplayOrder
			if oldOrder != newOrder {
				for i := range current.Spec.Environments {
					if i != idx && current.Spec.Environments[i].DisplayOrder == newOrder {
						current.Spec.Environments[i].DisplayOrder = oldOrder
						break
					}
				}
				current.Spec.Environments[idx].DisplayOrder = newOrder
			}
		}
		if req.Restricted != nil {
			current.Spec.Environments[idx].Restricted = *req.Restricted
		}

		if err := s.client.Update(ctx, &current); err != nil {
			return err
		}
		updated = current.Spec.Environments[idx]
		return nil
	})
	if err != nil {
		return mortisev1alpha1.ProjectEnvironment{}, err
	}
	return updated, nil
}

// DeleteProjectEnvironment removes an env from spec.environments. Any Apps
// that carry per-env overrides for the deleted environment are automatically
// stripped before the project update, so deletion always succeeds.
//
// DELETE /api/projects/{project}/environments/{name}
//
// @Summary Delete a project environment
// @Description Removes an environment from the project. Automatically strips per-env overrides from all apps referencing the deleted environment before removing it.
// @Tags environments
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param name path string true "Environment name"
// @Success 200 {object} map[string]string "Deletion confirmation"
// @Failure 400 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
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
	stripped, err := s.stripAppEnvOverrides(r.Context(), projectNs(project), envName)
	if err != nil {
		writeError(w, r, err)
		return
	}

	project.Spec.Environments = append(project.Spec.Environments[:idx], project.Spec.Environments[idx+1:]...)
	if err := s.client.Update(r.Context(), project); err != nil {
		if apierrors.IsForbidden(err) {
			writeJSON(w, http.StatusConflict, errorResponse{err.Error()})
			return
		}
		writeError(w, r, err)
		return
	}

	msg := "Deleted project environment " + envName
	if len(stripped) > 0 {
		msg += fmt.Sprintf(" (stripped overrides from %v)", stripped)
	}
	s.recordActivity(r, projectName, "delete", "environment", envName, msg, "")

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": envName})
}

type cloneProjectEnvRequest struct {
	Name         string `json:"name"`
	DisplayOrder int    `json:"displayOrder,omitempty"`
}

// CloneProjectEnvironment creates a new environment by copying the full
// configuration from an existing source environment. For every App in the
// project, CRD-level overrides (replicas, resources, probes, bindings,
// annotations) are cloned onto the new env entry, while Secret-level env vars
// (set via the UI/API) stay in envstore and are copied to the target Secret.
// Binding-sourced vars are excluded because the controller re-resolves them in
// the new namespace.
//
// POST /api/projects/{project}/environments/{source}/clone  { "name": "staging" }
//
// @Summary Clone a project environment
// @Description Creates a new environment pre-populated with the source's config for every app. CRD overrides are cloned on the App, while Secret-backed env vars remain in the Secret layer. Returns 409 Conflict if the target environment already exists on the project.
// @Tags environments
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param source path string true "Source environment name"
// @Param body body cloneProjectEnvRequest true "Target environment name and display order"
// @Success 201 {object} projectEnvResponse "Created"
// @Failure 400 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Failure 409 {object} errorResponse
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
	if msg := validateProjectEnvName(req.Name); msg != "" {
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
	if err := s.ensureProjectEnvNamespace(r.Context(), project, req.Name, false); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.client.Update(r.Context(), project); err != nil {
		_ = s.deleteProjectEnvNamespace(r.Context(), project, req.Name)
		writeError(w, r, err)
		return
	}
	if err := s.ensureProjectEnvNamespace(r.Context(), project, req.Name, true); err != nil {
		writeError(w, r, err)
		return
	}

	// Clone env overrides and Secret-backed env vars from source to target for every App.
	if err := s.cloneAppOverrides(r.Context(), projectName, projectNs(project), sourceName, req.Name); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.cloneCustomSecrets(r.Context(), constants.EnvNamespace(project.Name, sourceName), constants.EnvNamespace(project.Name, req.Name)); err != nil {
		writeError(w, r, err)
		return
	}

	s.recordActivity(r, projectName, "create", "environment", req.Name,
		fmt.Sprintf("Cloned environment %s from %s", req.Name, sourceName), "")

	writeJSON(w, http.StatusCreated, projectEnvResponse{
		Name:         req.Name,
		DisplayOrder: req.DisplayOrder,
		Health:       EnvHealthUnknown,
	})
}

func validateProjectEnvName(name string) string {
	if previewProjectEnvNameRegex.MatchString(name) {
		return "name uses reserved preview environment namespace pr-<number>"
	}
	return ""
}

// cloneAppOverrides copies the source environment's CRD-level overrides
// (env vars, bindings, resources, replicas, probes, schedule, annotations)
// to a new environment entry on every App in the project. Secret-backed env
// vars are copied separately via envstore so App.Spec remains the CRD source
// of truth. Binding-sourced vars are excluded because the controller
// re-resolves them in the target namespace.
func (s *Server) cloneAppOverrides(ctx context.Context, projectName, ns, sourceName, targetName string) error {
	var apps mortisev1alpha1.AppList
	if err := s.client.List(ctx, &apps, client.InNamespace(ns)); err != nil {
		return err
	}
	sourceEnvNs := constants.EnvNamespace(projectName, sourceName)
	targetEnvNs := constants.EnvNamespace(projectName, targetName)
	store := &envstore.Store{Client: s.client}

	for i := range apps.Items {
		appName := apps.Items[i].Name
		if err := s.cloneEnvToApp(ctx, ns, appName, sourceName, targetName, sourceEnvNs, store); err != nil {
			return err
		}
		if err := cloneAppEnvSecret(ctx, appName, sourceEnvNs, targetEnvNs, store); err != nil {
			return err
		}
	}
	return nil
}

const maxConflictRetries = 5

func (s *Server) cloneEnvToApp(ctx context.Context, ns, appName, sourceName, targetName, sourceEnvNs string, store *envstore.Store) error {
	var secretVars []envstore.Env
	if s.clientset != nil {
		secret, err := s.clientset.CoreV1().Secrets(sourceEnvNs).Get(ctx, envstore.AppEnvSecretName(appName), metav1.GetOptions{})
		if err == nil {
			secretVars = envstore.SecretToEnvs(secret)
		} else if !apierrors.IsNotFound(err) {
			return fmt.Errorf("read source env vars for app %q: %w", appName, err)
		}
	}
	if secretVars == nil {
		var err error
		secretVars, err = store.Get(ctx, sourceEnvNs, appName)
		if err != nil {
			return fmt.Errorf("read source env vars for app %q: %w", appName, err)
		}
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
		if !apierrors.IsConflict(updateErr) || attempt == maxConflictRetries-1 {
			return fmt.Errorf("clone env overrides for app %q: %w", appName, updateErr)
		}
	}
	return nil
}

// getProject is like resolveProject but returns the full Project pointer so
// callers can mutate and update the CRD.
func (s *Server) getProject(w http.ResponseWriter, r *http.Request) (*mortisev1alpha1.Project, bool) {
	return s.lookupProject(w, r, chi.URLParam(r, "project"))
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
func (s *Server) renameAppOverrides(ctx context.Context, projectName, ns, oldName, newName string) error {
	var apps mortisev1alpha1.AppList
	if err := s.client.List(ctx, &apps, client.InNamespace(ns)); err != nil {
		return err
	}
	store := &envstore.Store{Client: s.client}
	sourceEnvNs := constants.EnvNamespace(projectName, oldName)
	targetEnvNs := constants.EnvNamespace(projectName, newName)
	for i := range apps.Items {
		appName := apps.Items[i].Name
		if err := s.renameEnvOnApp(ctx, ns, appName, oldName, newName); err != nil {
			return err
		}
		if err := cloneAppEnvSecret(ctx, appName, sourceEnvNs, targetEnvNs, store); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) renameEnvOnApp(ctx context.Context, ns, appName, oldName, newName string) error {
	for attempt := range maxConflictRetries {
		var app mortisev1alpha1.App
		if err := s.client.Get(ctx, types.NamespacedName{Namespace: ns, Name: appName}, &app); err != nil {
			return fmt.Errorf("get app %q: %w", appName, err)
		}

		overrideIdx := -1
		for j := range app.Spec.Environments {
			if app.Spec.Environments[j].Name == oldName {
				overrideIdx = j
				break
			}
		}
		if overrideIdx < 0 {
			return nil
		}

		renamed := app.Spec.Environments[overrideIdx]
		renamed.Name = newName
		app.Spec.Environments[overrideIdx] = renamed

		updateErr := s.client.Update(ctx, &app)
		if updateErr == nil {
			return nil
		}
		if !apierrors.IsConflict(updateErr) || attempt == maxConflictRetries-1 {
			return fmt.Errorf("rename env override for app %q: %w", appName, updateErr)
		}
	}
	return nil
}

func cloneAppEnvSecret(ctx context.Context, appName, sourceEnvNs, targetEnvNs string, store *envstore.Store) error {
	exists, err := store.SecretExists(ctx, sourceEnvNs, appName)
	if err != nil {
		return fmt.Errorf("check source env secret for app %q: %w", appName, err)
	}
	if !exists {
		return nil
	}

	sourceVars, err := store.Get(ctx, sourceEnvNs, appName)
	if err != nil {
		return fmt.Errorf("read source env vars for app %q: %w", appName, err)
	}

	cloned := make([]envstore.Env, 0, len(sourceVars))
	for _, env := range sourceVars {
		if env.Source == "binding" {
			continue
		}
		cloned = append(cloned, env)
	}
	if len(cloned) == 0 {
		return nil
	}

	if err := store.Set(ctx, targetEnvNs, appName, cloned, nil); err != nil {
		return fmt.Errorf("copy env secret for app %q to %q: %w", appName, targetEnvNs, err)
	}
	return nil
}

// stripAppEnvOverrides removes the env override entry from every App in the
// project namespace that references envName. Uses RetryOnConflict per App to
// handle concurrent modifications. Returns the list of app names that had
// overrides stripped.
func (s *Server) stripAppEnvOverrides(ctx context.Context, ns, envName string) ([]string, error) {
	var apps mortisev1alpha1.AppList
	if err := s.client.List(ctx, &apps, client.InNamespace(ns)); err != nil {
		return nil, err
	}
	var stripped []string
	for i := range apps.Items {
		app := &apps.Items[i]
		has := false
		for _, env := range app.Spec.Environments {
			if env.Name == envName {
				has = true
				break
			}
		}
		if !has {
			continue
		}
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var fresh mortisev1alpha1.App
			if err := s.client.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
				return err
			}
			idx := -1
			for j, env := range fresh.Spec.Environments {
				if env.Name == envName {
					idx = j
					break
				}
			}
			if idx < 0 {
				return nil
			}
			fresh.Spec.Environments = append(fresh.Spec.Environments[:idx], fresh.Spec.Environments[idx+1:]...)
			return s.client.Update(ctx, &fresh)
		}); err != nil {
			return stripped, fmt.Errorf("strip env override from app %q: %w", app.Name, err)
		}
		stripped = append(stripped, app.Name)
	}
	return stripped, nil
}

func (s *Server) ensureProjectEnvNamespace(ctx context.Context, project *mortisev1alpha1.Project, envName string, active bool) error {
	nsName := constants.EnvNamespace(project.Name, envName)
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "mortise",
	}
	if active {
		labels[constants.ProjectLabel] = project.Name
		labels["mortise.dev/managed-by"] = "project"
		labels[constants.NamespaceRoleLabel] = constants.NamespaceRoleEnv
		labels[constants.EnvironmentLabel] = envName
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var existing corev1.Namespace
		err := s.client.Get(ctx, types.NamespacedName{Name: nsName}, &existing)
		if apierrors.IsNotFound(err) {
			isController := true
			blockOwnerDeletion := true
			createErr := s.client.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nsName,
					Labels: labels,
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion:         mortisev1alpha1.GroupVersion.String(),
						Kind:               "Project",
						Name:               project.Name,
						UID:                project.UID,
						Controller:         &isController,
						BlockOwnerDeletion: &blockOwnerDeletion,
					}},
				},
			})
			if apierrors.IsAlreadyExists(createErr) {
				return apierrors.NewConflict(corev1.Resource("namespaces"), nsName, createErr)
			}
			return createErr
		}
		if err != nil {
			return err
		}

		ownedByUs := false
		ownedByOther := ""
		for _, ref := range existing.OwnerReferences {
			if ref.APIVersion == mortisev1alpha1.GroupVersion.String() && ref.Kind == "Project" {
				if ref.UID == project.UID {
					ownedByUs = true
					break
				}
				ownedByOther = ref.Name
			}
		}
		if !ownedByUs {
			if existing.Labels["app.kubernetes.io/managed-by"] == "mortise" && existing.Labels[constants.ProjectLabel] == project.Name {
				ownedByUs = true
			}
		}
		if !ownedByUs {
			if ownedByOther != "" {
				return fmt.Errorf("namespace %q is already owned by Project %q", nsName, ownedByOther)
			}
			return fmt.Errorf("namespace %q already exists and is not managed by mortise", nsName)
		}

		changed := false
		if existing.Labels == nil {
			existing.Labels = map[string]string{}
		}
		for k, v := range labels {
			if existing.Labels[k] != v {
				existing.Labels[k] = v
				changed = true
			}
		}
		if !changed {
			return nil
		}
		return s.client.Update(ctx, &existing)
	})
}

func (s *Server) deleteProjectEnvNamespace(ctx context.Context, project *mortisev1alpha1.Project, envName string) error {
	var ns corev1.Namespace
	if err := s.client.Get(ctx, types.NamespacedName{Name: constants.EnvNamespace(project.Name, envName)}, &ns); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return s.client.Delete(ctx, &ns)
}

func (s *Server) cloneCustomSecrets(ctx context.Context, sourceEnvNs, targetEnvNs string) error {
	var secrets corev1.SecretList
	if err := s.client.List(ctx, &secrets, client.InNamespace(sourceEnvNs)); err != nil {
		return err
	}

	for i := range secrets.Items {
		src := &secrets.Items[i]
		appName := src.Labels[constants.AppNameLabel]
		if appName == "" || src.Labels["app.kubernetes.io/managed-by"] != "mortise" {
			continue
		}
		if isInternalAppEnvSecret(appName, src) {
			continue
		}

		var existing corev1.Secret
		getErr := s.client.Get(ctx, types.NamespacedName{Namespace: targetEnvNs, Name: src.Name}, &existing)
		if apierrors.IsNotFound(getErr) {
			copied := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:        src.Name,
					Namespace:   targetEnvNs,
					Labels:      copyStringMap(src.Labels),
					Annotations: copyStringMap(src.Annotations),
				},
				Type: src.Type,
				Data: copyBytesMap(src.Data),
			}
			if err := s.client.Create(ctx, copied); err != nil && !apierrors.IsAlreadyExists(err) {
				return err
			}
			continue
		}
		if getErr != nil {
			return getErr
		}
		existing.Labels = copyStringMap(src.Labels)
		existing.Annotations = copyStringMap(src.Annotations)
		existing.Type = src.Type
		existing.Data = copyBytesMap(src.Data)
		if err := s.client.Update(ctx, &existing); err != nil {
			return err
		}
	}
	return nil
}

func copyStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func copyBytesMap(src map[string][]byte) map[string][]byte {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string][]byte, len(src))
	for k, v := range src {
		buf := make([]byte, len(v))
		copy(buf, v)
		out[k] = buf
	}
	return out
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
		case mortisev1alpha1.AppPhaseBuilding, mortisev1alpha1.AppPhaseDeploying, mortisev1alpha1.AppPhasePending, mortisev1alpha1.AppPhaseDegraded:
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
