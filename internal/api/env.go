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
)

// envVarResponse is the JSON shape for a single env var.
type envVarResponse struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// patchEnvRequest is the JSON body for PATCH .../env.
type patchEnvRequest struct {
	Set   map[string]string `json:"set"`
	Unset []string          `json:"unset"`
}

// GetEnv returns env vars for a specific environment on an app.
func (s *Server) GetEnv(w http.ResponseWriter, r *http.Request) {
	app, env, ok := s.resolveAppEnv(w, r)
	if !ok {
		return
	}

	envSpec := findEnvironment(app, env)
	if envSpec == nil {
		writeJSON(w, http.StatusOK, []envVarResponse{})
		return
	}

	resp := make([]envVarResponse, 0, len(envSpec.Env))
	for _, v := range envSpec.Env {
		resp = append(resp, envVarResponse{Name: v.Name, Value: v.Value})
	}
	writeJSON(w, http.StatusOK, resp)
}

// PutEnv replaces all env vars for a specific environment on an app.
func (s *Server) PutEnv(w http.ResponseWriter, r *http.Request) {
	app, env, ok := s.resolveAppEnv(w, r)
	if !ok {
		return
	}

	var vars []envVarResponse
	if err := json.NewDecoder(r.Body).Decode(&vars); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}

	envVars := make([]mortisev1alpha1.EnvVar, len(vars))
	for i, v := range vars {
		envVars[i] = mortisev1alpha1.EnvVar{Name: v.Name, Value: v.Value}
	}

	setEnvVars(app, env, envVars)
	annotateEnvHash(app, env)

	if err := s.client.Update(r.Context(), app); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// PatchEnv does a partial update of env vars for a specific environment.
func (s *Server) PatchEnv(w http.ResponseWriter, r *http.Request) {
	app, env, ok := s.resolveAppEnv(w, r)
	if !ok {
		return
	}

	var req patchEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}

	envSpec := ensureEnvironment(app, env)

	// Apply unsets.
	unsetMap := make(map[string]bool, len(req.Unset))
	for _, k := range req.Unset {
		unsetMap[k] = true
	}
	filtered := envSpec.Env[:0]
	for _, v := range envSpec.Env {
		if !unsetMap[v.Name] {
			filtered = append(filtered, v)
		}
	}
	envSpec.Env = filtered

	// Apply sets (update existing or append).
	for k, v := range req.Set {
		found := false
		for i := range envSpec.Env {
			if envSpec.Env[i].Name == k {
				envSpec.Env[i].Value = v
				found = true
				break
			}
		}
		if !found {
			envSpec.Env = append(envSpec.Env, mortisev1alpha1.EnvVar{Name: k, Value: v})
		}
	}

	annotateEnvHash(app, env)

	if err := s.client.Update(r.Context(), app); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ImportEnv parses a .env file body (text/plain) and merges into the
// environment's env vars.
func (s *Server) ImportEnv(w http.ResponseWriter, r *http.Request) {
	app, env, ok := s.resolveAppEnv(w, r)
	if !ok {
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"failed to read body"})
		return
	}

	parsed := parseDotEnv(string(body))
	envSpec := ensureEnvironment(app, env)

	for k, v := range parsed {
		found := false
		for i := range envSpec.Env {
			if envSpec.Env[i].Name == k {
				envSpec.Env[i].Value = v
				found = true
				break
			}
		}
		if !found {
			envSpec.Env = append(envSpec.Env, mortisev1alpha1.EnvVar{Name: k, Value: v})
		}
	}

	annotateEnvHash(app, env)

	if err := s.client.Update(r.Context(), app); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "imported", "count": fmt.Sprintf("%d", len(parsed))})
}

// resolveAppEnv reads the project, app, and environment query param. It fetches
// the App CRD and returns it along with the environment name.
func (s *Server) resolveAppEnv(w http.ResponseWriter, r *http.Request) (*mortisev1alpha1.App, string, bool) {
	ns, ok := s.resolveProject(w, r)
	if !ok {
		return nil, "", false
	}
	appName := chi.URLParam(r, "app")
	env := r.URL.Query().Get("environment")
	if env == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"environment query parameter is required"})
		return nil, "", false
	}

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
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

	// Build a deterministic string from env vars.
	var b strings.Builder
	for _, v := range env.Env {
		b.WriteString(v.Name)
		b.WriteByte('=')
		b.WriteString(v.Value)
		b.WriteByte('\n')
	}
	hash := sha256.Sum256([]byte(b.String()))

	if env.Annotations == nil {
		env.Annotations = make(map[string]string)
	}
	env.Annotations["mortise.dev/env-hash"] = fmt.Sprintf("%x", hash[:8])
}

// parseDotEnv parses KEY=value lines from a .env file string. Blank lines and
// lines starting with # are skipped. Values may be optionally quoted.
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
		// Strip optional surrounding quotes.
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		}
		result[key] = val
	}
	return result
}
