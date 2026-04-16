package api

import (
	"encoding/json"
	"net/http"

	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

// deployRequest is the JSON body for the deploy webhook.
type deployRequest struct {
	App         string `json:"app"`
	Namespace   string `json:"namespace"`
	Environment string `json:"environment"`
	Image       string `json:"image"`
}

// Deploy handles POST /api/deploy. It patches the App CRD's source.image field,
// which triggers the controller to reconcile a new deployment.
func (s *Server) Deploy(w http.ResponseWriter, r *http.Request) {
	var req deployRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}

	if req.App == "" || req.Image == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"app and image are required"})
		return
	}
	if req.Namespace == "" {
		req.Namespace = defaultNamespace
	}

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: req.App, Namespace: req.Namespace}, &app); err != nil {
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
		"app":    req.App,
		"image":  req.Image,
	})
}
