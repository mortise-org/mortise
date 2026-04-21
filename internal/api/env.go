package api

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/constants"
	"github.com/MC-Meesh/mortise/internal/envstore"
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
	app, envName, ok := s.resolveAppEnv(w, r)
	if !ok {
		return
	}

	envNs := envNamespace(app, envName)
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
	app, envName, ok := s.resolveAppEnv(w, r)
	if !ok {
		return
	}

	var vars []envVarResponse
	if err := json.NewDecoder(r.Body).Decode(&vars); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}

	envNs := envNamespace(app, envName)
	store := &envstore.Store{Client: s.client}

	envVars := make([]envstore.Env, len(vars))
	for i, v := range vars {
		envVars[i] = envstore.Env{Name: v.Name, Value: v.Value, Source: "user"}
	}

	projectName, _ := constants.ProjectFromControlNs(app.Namespace)
	labels := map[string]string{
		constants.ProjectLabel:     projectName,
		constants.EnvironmentLabel: envName,
		"app.kubernetes.io/name":   app.Name,
	}
	if err := store.Set(r.Context(), envNs, app.Name, envVars, labels); err != nil {
		writeError(w, err)
		return
	}

	// Also update the CRD spec so the controller picks up the change and
	// triggers a Deployment rollout.
	crdEnvVars := make([]mortisev1alpha1.EnvVar, len(vars))
	for i, v := range vars {
		crdEnvVars[i] = mortisev1alpha1.EnvVar{Name: v.Name, Value: v.Value}
	}
	setEnvVars(app, envName, crdEnvVars)
	annotateEnvHash(app, envName)
	if err := s.client.Update(r.Context(), app); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// PatchEnv does a partial update of env vars for a specific environment.
// Reads existing vars from the Secret, applies changes, writes back.
func (s *Server) PatchEnv(w http.ResponseWriter, r *http.Request) {
	app, envName, ok := s.resolveAppEnv(w, r)
	if !ok {
		return
	}

	var req patchEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}

	envNs := envNamespace(app, envName)
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

	projectName, _ := constants.ProjectFromControlNs(app.Namespace)
	labels := map[string]string{
		constants.ProjectLabel:     projectName,
		constants.EnvironmentLabel: envName,
		"app.kubernetes.io/name":   app.Name,
	}
	if err := store.Set(r.Context(), envNs, app.Name, result, labels); err != nil {
		writeError(w, err)
		return
	}

	// Sync back to CRD spec for controller rollout trigger.
	crdVars := make([]mortisev1alpha1.EnvVar, len(result))
	for i, e := range result {
		crdVars[i] = mortisev1alpha1.EnvVar{Name: e.Name, Value: e.Value}
	}
	setEnvVars(app, envName, crdVars)
	annotateEnvHash(app, envName)
	if err := s.client.Update(r.Context(), app); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ImportEnv parses a .env file body and merges into the environment's env vars.
func (s *Server) ImportEnv(w http.ResponseWriter, r *http.Request) {
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

	envNs := envNamespace(app, envName)
	store := &envstore.Store{Client: s.client}

	var vars []envstore.Env
	for k, v := range parsed {
		vars = append(vars, envstore.Env{Name: k, Value: v, Source: "user"})
	}

	projectName, _ := constants.ProjectFromControlNs(app.Namespace)
	labels := map[string]string{
		constants.ProjectLabel:     projectName,
		constants.EnvironmentLabel: envName,
		"app.kubernetes.io/name":   app.Name,
	}
	if err := store.Merge(r.Context(), envNs, app.Name, vars, labels); err != nil {
		writeError(w, err)
		return
	}

	// Read back merged result for CRD sync.
	merged, _ := store.Get(r.Context(), envNs, app.Name)
	crdVars := make([]mortisev1alpha1.EnvVar, len(merged))
	for i, e := range merged {
		crdVars[i] = mortisev1alpha1.EnvVar{Name: e.Name, Value: e.Value}
	}
	setEnvVars(app, envName, crdVars)
	annotateEnvHash(app, envName)
	if err := s.client.Update(r.Context(), app); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "imported", "count": fmt.Sprintf("%d", len(parsed))})
}

// GetSharedVars returns shared env vars for a project environment.
func (s *Server) GetSharedVars(w http.ResponseWriter, r *http.Request) {
	project, ok := s.getProject(w, r)
	if !ok {
		return
	}
	env := r.URL.Query().Get("environment")
	if env == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"environment query parameter is required"})
		return
	}

	envNs := constants.EnvNamespace(project.Name, env)
	store := &envstore.Store{Client: s.client}
	envs, err := store.GetShared(r.Context(), envNs)
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

// PutSharedVars replaces all shared env vars for a project environment.
func (s *Server) PutSharedVars(w http.ResponseWriter, r *http.Request) {
	project, ok := s.getProject(w, r)
	if !ok {
		return
	}
	env := r.URL.Query().Get("environment")
	if env == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"environment query parameter is required"})
		return
	}

	var vars []envVarResponse
	if err := json.NewDecoder(r.Body).Decode(&vars); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}

	envNs := constants.EnvNamespace(project.Name, env)
	store := &envstore.Store{Client: s.client}

	envVars := make([]envstore.Env, len(vars))
	for i, v := range vars {
		envVars[i] = envstore.Env{Name: v.Name, Value: v.Value, Source: "shared"}
	}

	labels := map[string]string{
		constants.ProjectLabel:     project.Name,
		constants.EnvironmentLabel: env,
	}
	if err := store.SetShared(r.Context(), envNs, envVars, labels); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// envNamespace returns the workload namespace for an app + environment.
func envNamespace(app *mortisev1alpha1.App, envName string) string {
	projectName, _ := constants.ProjectFromControlNs(app.Namespace)
	return constants.EnvNamespace(projectName, envName)
}

// resolveAppEnv reads the project, app, and environment query param.
func (s *Server) resolveAppEnv(w http.ResponseWriter, r *http.Request) (*mortisev1alpha1.App, string, bool) {
	project, ok := s.getProject(w, r)
	if !ok {
		return nil, "", false
	}
	appName := chi.URLParam(r, "app")
	env := r.URL.Query().Get("environment")
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

// setEnvVars replaces all env vars for the named environment.
func setEnvVars(app *mortisev1alpha1.App, envName string, vars []mortisev1alpha1.EnvVar) {
	env := ensureEnvironment(app, envName)
	env.Env = vars
}

// annotateEnvHash sets the mortise.dev/env-hash annotation on the environment
// to trigger a rolling restart when env vars change.
func annotateEnvHash(app *mortisev1alpha1.App, envName string) {
	env := findEnvironment(app, envName)
	if env == nil {
		return
	}
	var b strings.Builder
	for _, v := range env.Env {
		b.WriteString(v.Name)
		b.WriteByte('=')
		b.WriteString(v.Value)
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	hash := fmt.Sprintf("%x", sum[:8])
	if env.Annotations == nil {
		env.Annotations = make(map[string]string)
	}
	env.Annotations["mortise.dev/env-hash"] = hash
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
