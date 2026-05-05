package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
)

// buildArgResponse is the JSON shape for a single build arg.
type buildArgResponse struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// GetBuildArgs returns the build args for an app.
//
// GET /api/projects/{project}/apps/{app}/build-args
//
// @Summary Get build args for an app
// @Description Returns all build arguments for an app (from spec.source.build.args)
// @Tags build-args
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Success 200 {array} buildArgResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app}/build-args [get]
func (s *Server) GetBuildArgs(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionRead) {
		return
	}
	app, ok := s.resolveApp(w, r)
	if !ok {
		return
	}

	args := buildArgsFromApp(app)
	writeJSON(w, http.StatusOK, args)
}

// PutBuildArgs replaces all build args for an app.
//
// PUT /api/projects/{project}/apps/{app}/build-args
//
// @Summary Replace build args for an app
// @Description Replaces all build arguments for an app (spec.source.build.args)
// @Tags build-args
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Param body body []buildArgResponse true "Build arguments"
// @Success 200 {array} buildArgResponse
// @Failure 400 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app}/build-args [put]
func (s *Server) PutBuildArgs(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionUpdate) {
		return
	}
	app, ok := s.resolveApp(w, r)
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

	if app.Spec.Source.Build == nil {
		app.Spec.Source.Build = &mortisev1alpha1.Build{}
	}
	app.Spec.Source.Build.Args = args

	if err := s.client.Update(r.Context(), app); err != nil {
		writeError(w, err)
		return
	}

	s.recordActivity(r, projectName, "update", "app", app.Name, "Updated build args for "+app.Name, "")

	writeJSON(w, http.StatusOK, buildArgsFromApp(app))
}

// resolveApp fetches the App CRD by project and app URL params.
func (s *Server) resolveApp(w http.ResponseWriter, r *http.Request) (*mortisev1alpha1.App, bool) {
	project, ok := s.getProject(w, r)
	if !ok {
		return nil, false
	}
	appName := chi.URLParam(r, "app")

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), client.ObjectKey{Name: appName, Namespace: projectNs(project)}, &app); err != nil {
		writeError(w, err)
		return nil, false
	}
	return &app, true
}

func buildArgsFromApp(app *mortisev1alpha1.App) []buildArgResponse {
	if app.Spec.Source.Build == nil || len(app.Spec.Source.Build.Args) == 0 {
		return []buildArgResponse{}
	}
	resp := make([]buildArgResponse, 0, len(app.Spec.Source.Build.Args))
	for k, v := range app.Spec.Source.Build.Args {
		resp = append(resp, buildArgResponse{Name: k, Value: v})
	}
	return resp
}
