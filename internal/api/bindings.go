package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
)

// bindingEdge represents a single from-app→to-app binding resolved for a
// specific environment. The canvas renders edges directly from this list so
// the UI doesn't need to merge per-env overrides itself.
type bindingEdge struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Environment string `json:"environment"`
}

// ListBindings returns every binding edge in the project for the requested
// environment. Apps with `enabled: false` for the env are skipped.
//
// GET /api/projects/{project}/bindings?environment=staging
//
// @Summary List bindings for a project environment
// @Description Returns all binding edges (from-app to to-app) for the specified environment
// @Tags bindings
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param environment query string true "Environment name"
// @Success 200 {array} bindingEdge
// @Failure 400 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Router /projects/{project}/bindings [get]
func (s *Server) ListBindings(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionRead) {
		return
	}
	project, ok := s.getProject(w, r)
	if !ok {
		return
	}
	envName := queryEnv(r)
	if envName == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"environment query parameter is required"})
		return
	}
	if indexOfEnv(project, envName) < 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			"environment \"" + envName + "\" is not declared on project \"" + project.Name + "\"",
		})
		return
	}

	var apps mortisev1alpha1.AppList
	if err := s.client.List(r.Context(), &apps, client.InNamespace(projectNs(project))); err != nil {
		writeError(w, err)
		return
	}

	edges := make([]bindingEdge, 0)
	for i := range apps.Items {
		app := &apps.Items[i]
		if !appParticipatesInEnv(app, envName) {
			continue
		}
		for _, b := range bindingsForEnv(app, envName) {
			edges = append(edges, bindingEdge{
				From:        app.Name,
				To:          b.Ref,
				Environment: envName,
			})
		}
	}
	writeJSON(w, http.StatusOK, edges)
}

// bindingsForEnv returns the effective binding list for (app, env). If the app
// has a per-env override for this env, its Bindings win (empty override means
// no bindings for that env). Otherwise falls back to the app's shared
// bindings (none today — Environment-level is the only tier).
func bindingsForEnv(app *mortisev1alpha1.App, envName string) []mortisev1alpha1.Binding {
	for _, env := range app.Spec.Environments {
		if env.Name == envName {
			return env.Bindings
		}
	}
	return nil
}
