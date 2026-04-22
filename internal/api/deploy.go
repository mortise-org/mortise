package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/authz"
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
func (s *Server) Deploy(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	appName := chi.URLParam(r, "app")

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
		if !s.authorize(w, r, authz.Resource{Kind: "app", Namespace: ns, Project: projectName}, authz.ActionUpdate) {
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
			if !s.validateProjectDeployToken(r, ns, projectName) {
				writeJSON(w, http.StatusUnauthorized, errorResponse{"invalid deploy token"})
				return
			}
		} else {
			// Per-app+env token: requires environment and scoped to one app.
			if req.Environment == "" {
				writeJSON(w, http.StatusBadRequest, errorResponse{"environment is required when using a deploy token"})
				return
			}
			if !s.validateDeployToken(r, ns, appName, req.Environment) {
				writeJSON(w, http.StatusUnauthorized, errorResponse{"invalid deploy token"})
				return
			}
		}
	}

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	app.Spec.Source.Image = req.Image
	if err := s.client.Update(r.Context(), &app); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "deployed",
		"app":    appName,
		"image":  req.Image,
	})
}
