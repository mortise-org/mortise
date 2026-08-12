package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
)

// buildArgResponse is the JSON shape for a single build arg.
type buildArgResponse struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// GetBuildArgs returns the build args for an app's environment.
//
// GET /api/projects/{project}/apps/{app}/build-args?environment=production
//
// @Summary Get build args for an app environment
// @Description Returns all build arguments for a specific environment on an app
// @Tags build-args
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Param environment query string true "Environment name"
// @Success 200 {array} buildArgResponse
// @Failure 400 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app}/build-args [get]
func (s *Server) GetBuildArgs(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionRead) {
		return
	}
	app, envName, ok := s.resolveAppEnv(w, r)
	if !ok {
		return
	}

	args := buildArgsForEnv(app, envName)
	writeJSON(w, http.StatusOK, args)
}

// PutBuildArgs replaces all build args for an app's environment.
//
// PUT /api/projects/{project}/apps/{app}/build-args?environment=production
//
// @Summary Replace build args for an app environment
// @Description Replaces all build arguments for a specific environment on an app
// @Tags build-args
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Param environment query string true "Environment name"
// @Param body body []buildArgResponse true "Build arguments"
// @Success 200 {array} buildArgResponse
// @Failure 400 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app}/build-args [put]
func (s *Server) PutBuildArgs(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName, Environment: queryEnv(r)}, authz.ActionUpdate) {
		return
	}
	app, envName, ok := s.resolveAppEnv(w, r)
	if !ok {
		return
	}

	var vars []buildArgResponse
	if err := json.NewDecoder(r.Body).Decode(&vars); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}

	args := make(map[string]string, len(vars))
	for _, v := range vars {
		if v.Name == "" {
			continue
		}
		args[v.Name] = v.Value
	}

	// Re-read inside the retry so a concurrent App write (controller or
	// another request) doesn't surface as a user-facing 409.
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh mortisev1alpha1.App
		if err := s.client.Get(r.Context(), types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
			return err
		}
		ensureEnvironment(&fresh, envName).BuildArgs = args
		if err := s.client.Update(r.Context(), &fresh); err != nil {
			return err
		}
		*app = fresh
		return nil
	}); err != nil {
		writeError(w, r, err)
		return
	}

	s.recordActivity(r, projectName, "update", "app", app.Name, "Updated build args for "+app.Name+" in "+envName, "")

	writeJSON(w, http.StatusOK, buildArgsForEnv(app, envName))
}

func buildArgsForEnv(app *mortisev1alpha1.App, envName string) []buildArgResponse {
	for _, env := range app.Spec.Environments {
		if env.Name == envName && len(env.BuildArgs) > 0 {
			resp := make([]buildArgResponse, 0, len(env.BuildArgs))
			for k, v := range env.BuildArgs {
				resp = append(resp, buildArgResponse{Name: k, Value: v})
			}
			return resp
		}
	}
	return []buildArgResponse{}
}
