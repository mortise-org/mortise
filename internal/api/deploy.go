package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
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
func (s *Server) Deploy(w http.ResponseWriter, r *http.Request) {
	ns, ok := s.resolveProject(w, r)
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
