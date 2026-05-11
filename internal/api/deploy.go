package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
)

// deployRequest is the JSON body for POST /api/projects/{p}/apps/{a}/deploy.
// App + project are in the URL; environment + image come from the body.
type deployRequest struct {
	Environment string `json:"environment"`
	Image       string `json:"image"`
}

// Deploy handles the deploy webhook for a given App. It patches the App CRD's
// source.image field, which triggers the controller to reconcile a new
// deployment.
//
// Auth: accepts either a valid JWT (user session) or a deploy token (CI).
// Deploy tokens are scoped to a specific app+environment; the handler rejects
// mismatches.
//
// @Summary Deploy an app
// @Description Trigger a deploy by patching the App's source image. Accepts either a JWT (user session) or a deploy token (CI) for authentication.
// @Tags deploy
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Param body body deployRequest true "Deploy details"
// @Success 200 {object} map[string]string
// @Failure 400 {object} errorResponse
// @Failure 401 {object} errorResponse
// @Router /projects/{project}/apps/{app}/deploy [post]
func (s *Server) Deploy(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	appName := chi.URLParam(r, "app")
	actorOverride := ""

	var req deployRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if req.Image == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"image is required"})
		return
	}

	// Check auth: JWT principal (policy-checked) or deploy token (inline check).
	if p := PrincipalFromContext(r.Context()); p != nil {
		if !s.authorize(w, r, authz.Resource{Kind: "app", Namespace: ns, Project: projectName, Environment: req.Environment}, authz.ActionUpdate) {
			return
		}
	} else {
		// No JWT principal — try deploy token auth.
		header := r.Header.Get("Authorization")
		token := strings.TrimPrefix(header, "Bearer ")
		if !strings.HasPrefix(token, deployTokenPrefix) {
			writeJSON(w, http.StatusUnauthorized, errorResponse{"missing or invalid authorization"})
			return
		}

		if strings.HasPrefix(token, projectDeployTokenPrefix) {
			// Project-scoped token: grants deploy to any app in the project.
			ok, tokenName := s.validateProjectDeployToken(r, ns, projectName)
			if !ok {
				writeJSON(w, http.StatusUnauthorized, errorResponse{"invalid deploy token"})
				return
			}
			actorOverride = "token:" + tokenName
		} else {
			// Per-app+env token: requires environment and scoped to one app.
			if req.Environment == "" {
				writeJSON(w, http.StatusBadRequest, errorResponse{"environment is required when using a deploy token"})
				return
			}
			ok, tokenName := s.validateDeployToken(r, ns, appName, req.Environment)
			if !ok {
				writeJSON(w, http.StatusUnauthorized, errorResponse{"invalid deploy token"})
				return
			}
			actorOverride = "token:" + tokenName
		}
	}

	project, ok := s.getProject(w, r)
	if !ok {
		return
	}
	if req.Environment != "" {
		if msg := validateDNSLabel("environment", req.Environment, 63); msg != "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{msg})
			return
		}
		if indexOfEnv(project, req.Environment) < 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{fmt.Sprintf(
				"environment %q is not declared on project %q — add it via POST /api/projects/%s/environments first",
				req.Environment, project.Name, project.Name)})
			return
		}
	}

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
		writeError(w, r, err)
		return
	}
	if p := PrincipalFromContext(r.Context()); p != nil && req.Environment == "" {
		for _, env := range project.Spec.Environments {
			if !appParticipatesInEnv(&app, env.Name) {
				continue
			}
			if !s.authorize(w, r, authz.Resource{Kind: "app", Namespace: ns, Project: projectName, Environment: env.Name}, authz.ActionUpdate) {
				return
			}
		}
	}
	if req.Environment != "" && !appParticipatesInEnv(&app, req.Environment) {
		writeJSON(w, http.StatusNotFound, errorResponse{fmt.Sprintf("app %q is not enabled in environment %q", app.Name, req.Environment)})
		return
	}

	if req.Environment != "" {
		env := ensureEnvironment(&app, req.Environment)
		env.Image = req.Image
	} else {
		app.Spec.Source.Image = req.Image
	}
	if err := s.client.Update(r.Context(), &app); err != nil {
		writeError(w, r, err)
		return
	}

	msg := fmt.Sprintf("Deployed %s", appName)
	if req.Environment != "" {
		msg = fmt.Sprintf("Deployed %s to %s", appName, req.Environment)
	}
	s.recordActivity(r, projectName, "deploy", "app", appName, msg, actorOverride)

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "deployed",
		"app":    appName,
		"image":  req.Image,
	})
}
