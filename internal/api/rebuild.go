package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

// Rebuild triggers a fresh build from the latest git commit.
// It clears lastBuiltSHA so the reconciler treats the current revision as new,
// and resets the phase from Failed/CrashLooping so the build guard doesn't skip it.
//
// POST /api/projects/{project}/apps/{app}/rebuild
func (s *Server) Rebuild(w http.ResponseWriter, r *http.Request) {
	ns, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	appName := chi.URLParam(r, "app")

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	if app.Spec.Source.Type != mortisev1alpha1.SourceTypeGit {
		writeJSON(w, http.StatusBadRequest, errorResponse{"rebuild is only supported for git-source apps"})
		return
	}

	// Clear lastBuiltSHA so the reconciler sees the revision as new.
	app.Status.LastBuiltSHA = ""
	app.Status.Phase = mortisev1alpha1.AppPhaseBuilding
	app.Status.Conditions = nil
	if err := s.client.Status().Update(r.Context(), &app); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "rebuilding"})
}
