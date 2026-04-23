package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/envstore"
)

// envVarResponse is the JSON shape for a single env var.
type envVarResponse struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Source string `json:"source,omitempty"`
}

// patchEnvRequest is the JSON body for PATCH .../env.
type patchEnvRequest struct {
	Set   map[string]string `json:"set"`
	Unset []string          `json:"unset"`
}

// GetEnv returns env vars for a specific environment on an app.
// Reads from the {app}-env Secret in the env namespace.
func (s *Server) GetEnv(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionRead) {
		return
	}
	app, envName, ok := s.resolveAppEnv(w, r)
	if !ok {
		return
	}

	envNs, err := envNamespace(app, envName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}
	store := &envstore.Store{Client: s.client}
	envs, err := store.Get(r.Context(), envNs, app.Name)
	if err != nil {
		writeError(w, err)
		return
	}

	resp := make([]envVarResponse, 0, len(envs))
	for _, e := range envs {
		resp = append(resp, envVarResponse{Name: e.Name, Value: e.Value, Source: e.Source})
	}
	writeJSON(w, http.StatusOK, resp)
}

// PutEnv replaces all env vars for a specific environment on an app.
// Writes to the {app}-env Secret in the env namespace.
func (s *Server) PutEnv(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionUpdate) {
		return
	}
	app, envName, ok := s.resolveAppEnv(w, r)
	if !ok {
		return
	}

	var vars []envVarResponse
	if err := json.NewDecoder(r.Body).Decode(&vars); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}

	envNs, err := envNamespace(app, envName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}
	store := &envstore.Store{Client: s.client}

	// Read existing vars so we can preserve non-user sources (binding, generated, shared).
	existing, err := store.Get(r.Context(), envNs, app.Name)
	if err != nil {
		writeError(w, err)
		return
	}

	// Start with non-user vars from the existing Secret.
	var merged []envstore.Env
	for _, e := range existing {
		if e.Source != "user" && e.Source != "" {
			merged = append(merged, e)
		}
	}
	// Append the new user vars.
	for _, v := range vars {
		merged = append(merged, envstore.Env{Name: v.Name, Value: v.Value, Source: "user"})
	}

	projectName, ok2 := constants.ProjectFromControlNs(app.Namespace)
	if !ok2 {
		writeJSON(w, http.StatusInternalServerError, errorResponse{fmt.Sprintf("app %q not in a control namespace (%q)", app.Name, app.Namespace)})
		return
	}
	labels := map[string]string{
		constants.ProjectLabel:     projectName,
		constants.EnvironmentLabel: envName,
		constants.AppNameLabel:     app.Name,
	}
	if err := store.Set(r.Context(), envNs, app.Name, merged, labels); err != nil {
		writeError(w, err)
		return
	}

	if err := pokeAppForReconcile(r.Context(), s.client, app); err != nil {
		writeError(w, err)
		return
	}

	s.recordActivity(r, projectName, "update", "app", app.Name, "Updated env vars for "+app.Name+" in "+envName, "")

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// PatchEnv does a partial update of env vars for a specific environment.
// Reads existing vars from the Secret, applies changes, writes back.
func (s *Server) PatchEnv(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionUpdate) {
		return
	}
	app, envName, ok := s.resolveAppEnv(w, r)
	if !ok {
		return
	}

	var req patchEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}

	envNs, err := envNamespace(app, envName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}
	store := &envstore.Store{Client: s.client}

	// Read existing vars from Secret.
	existing, err := store.Get(r.Context(), envNs, app.Name)
	if err != nil {
		writeError(w, err)
		return
	}

	// Apply unsets.
	unsetMap := make(map[string]bool, len(req.Unset))
	for _, k := range req.Unset {
		unsetMap[k] = true
	}
	var result []envstore.Env
	for _, e := range existing {
		if !unsetMap[e.Name] {
			result = append(result, e)
		}
	}

	// Apply sets.
	for k, v := range req.Set {
		found := false
		for i := range result {
			if result[i].Name == k {
				result[i].Value = v
				result[i].Source = "user"
				found = true
				break
			}
		}
		if !found {
			result = append(result, envstore.Env{Name: k, Value: v, Source: "user"})
		}
	}

	projectName, ok2 := constants.ProjectFromControlNs(app.Namespace)
	if !ok2 {
		writeJSON(w, http.StatusInternalServerError, errorResponse{fmt.Sprintf("app %q not in a control namespace (%q)", app.Name, app.Namespace)})
		return
	}
	labels := map[string]string{
		constants.ProjectLabel:     projectName,
		constants.EnvironmentLabel: envName,
		constants.AppNameLabel:     app.Name,
	}
	if err := store.Set(r.Context(), envNs, app.Name, result, labels); err != nil {
		writeError(w, err)
		return
	}

	if err := pokeAppForReconcile(r.Context(), s.client, app); err != nil {
		writeError(w, err)
		return
	}

	s.recordActivity(r, projectName, "update", "app", app.Name, "Patched env vars for "+app.Name+" in "+envName, "")

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ImportEnv parses a .env file body and merges into the environment's env vars.
func (s *Server) ImportEnv(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionUpdate) {
		return
	}
	app, envName, ok := s.resolveAppEnv(w, r)
	if !ok {
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"failed to read body"})
		return
	}

	parsed := parseDotEnv(string(body))

	envNs, err := envNamespace(app, envName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}
	store := &envstore.Store{Client: s.client}

	var vars []envstore.Env
	for k, v := range parsed {
		vars = append(vars, envstore.Env{Name: k, Value: v, Source: "user"})
	}

	projectName, ok2 := constants.ProjectFromControlNs(app.Namespace)
	if !ok2 {
		writeJSON(w, http.StatusInternalServerError, errorResponse{fmt.Sprintf("app %q not in a control namespace (%q)", app.Name, app.Namespace)})
		return
	}
	labels := map[string]string{
		constants.ProjectLabel:     projectName,
		constants.EnvironmentLabel: envName,
		constants.AppNameLabel:     app.Name,
	}
	if err := store.Merge(r.Context(), envNs, app.Name, vars, labels); err != nil {
		writeError(w, err)
		return
	}

	if err := pokeAppForReconcile(r.Context(), s.client, app); err != nil {
		writeError(w, err)
		return
	}

	s.recordActivity(r, projectName, "update", "app", app.Name, "Imported env vars for "+app.Name+" in "+envName, "")

	writeJSON(w, http.StatusOK, map[string]string{"status": "imported", "count": fmt.Sprintf("%d", len(parsed))})
}

// GetSharedVars returns shared env vars for a project.
// Reads from the shared-vars Secret in the control namespace (source of truth).
// The controller materializes these into shared-env in each env namespace.
func (s *Server) GetSharedVars(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionRead) {
		return
	}
	project, ok := s.getProject(w, r)
	if !ok {
		return
	}

	controlNs := constants.ControlNamespace(project.Name)
	store := &envstore.Store{Client: s.client}
	envs, err := store.GetSharedSource(r.Context(), controlNs)
	if err != nil {
		writeError(w, err)
		return
	}

	resp := make([]envVarResponse, 0, len(envs))
	for _, e := range envs {
		resp = append(resp, envVarResponse{Name: e.Name, Value: e.Value, Source: e.Source})
	}
	writeJSON(w, http.StatusOK, resp)
}

// PutSharedVars replaces all shared env vars for a project.
// Writes to the shared-vars Secret in the control namespace.
// The controller materializes these into shared-env in each env namespace
// on the next reconcile of any app in the project.
func (s *Server) PutSharedVars(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionUpdate) {
		return
	}
	project, ok := s.getProject(w, r)
	if !ok {
		return
	}

	var vars []envVarResponse
	if err := json.NewDecoder(r.Body).Decode(&vars); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}

	controlNs := constants.ControlNamespace(project.Name)
	store := &envstore.Store{Client: s.client}

	envVars := make([]envstore.Env, len(vars))
	for i, v := range vars {
		envVars[i] = envstore.Env{Name: v.Name, Value: v.Value, Source: "shared"}
	}

	labels := map[string]string{
		constants.ProjectLabel: project.Name,
	}
	if err := store.SetSharedSource(r.Context(), controlNs, envVars, labels); err != nil {
		writeError(w, err)
		return
	}

	// Poke all apps in the project so the controller re-reconciles and
	// materializes the updated shared vars into each env namespace.
	var apps mortisev1alpha1.AppList
	if err := s.client.List(r.Context(), &apps, client.InNamespace(controlNs)); err != nil {
		writeError(w, err)
		return
	}
	for i := range apps.Items {
		if err := pokeAppForReconcile(r.Context(), s.client, &apps.Items[i]); err != nil {
			writeError(w, err)
			return
		}
	}

	s.recordActivity(r, project.Name, "update", "project", project.Name, "Updated shared variables", "")

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// envNamespace returns the workload namespace for an app + environment.
func envNamespace(app *mortisev1alpha1.App, envName string) (string, error) {
	projectName, ok := constants.ProjectFromControlNs(app.Namespace)
	if !ok {
		return "", fmt.Errorf("app %q not in a control namespace (%q)", app.Name, app.Namespace)
	}
	return constants.EnvNamespace(projectName, envName), nil
}

// resolveAppEnv reads the project, app, and environment query param.
func (s *Server) resolveAppEnv(w http.ResponseWriter, r *http.Request) (*mortisev1alpha1.App, string, bool) {
	project, ok := s.getProject(w, r)
	if !ok {
		return nil, "", false
	}
	appName := chi.URLParam(r, "app")
	env := queryEnv(r)
	if env == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"environment query parameter is required"})
		return nil, "", false
	}
	if indexOfEnv(project, env) < 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{fmt.Sprintf(
			"environment %q is not declared on project %q — add it via POST /api/projects/%s/environments first",
			env, project.Name, project.Name)})
		return nil, "", false
	}

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: projectNs(project)}, &app); err != nil {
		writeError(w, err)
		return nil, "", false
	}
	return &app, env, true
}

// ensureEnvironment returns a pointer to the named environment, creating it if
// it doesn't exist.
func ensureEnvironment(app *mortisev1alpha1.App, name string) *mortisev1alpha1.Environment {
	for i := range app.Spec.Environments {
		if app.Spec.Environments[i].Name == name {
			return &app.Spec.Environments[i]
		}
	}
	app.Spec.Environments = append(app.Spec.Environments, mortisev1alpha1.Environment{Name: name})
	return &app.Spec.Environments[len(app.Spec.Environments)-1]
}

// pokeAppForReconcile stamps a timestamp annotation on the App CRD so the
// controller re-reconciles (picking up the latest Secret contents). The
// Secret is the source of truth — we never sync env vars back to the CRD spec.
func pokeAppForReconcile(ctx context.Context, k8s client.Client, app *mortisev1alpha1.App) error {
	if app.Annotations == nil {
		app.Annotations = make(map[string]string)
	}
	app.Annotations["mortise.dev/env-updated"] = fmt.Sprintf("%d", time.Now().UnixMilli())
	return k8s.Update(ctx, app)
}

// parseDotEnv parses KEY=value lines from a .env file string.
func parseDotEnv(content string) map[string]string {
	result := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 1 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		}
		result[key] = val
	}
	return result
}
