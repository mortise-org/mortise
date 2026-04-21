package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/authz"
	"github.com/MC-Meesh/mortise/internal/constants"
)

type rollbackRequest struct {
	Environment string `json:"environment"`
	Index       int    `json:"index"`
}

// Rollback handles POST /api/projects/{p}/apps/{a}/rollback.
// It reads the deploy history for the given environment, patches the
// Deployment back to the image at the specified history index, and returns
// the DeployRecord that was rolled back to.
func (s *Server) Rollback(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "app", Namespace: ns}, authz.ActionUpdate) {
		return
	}
	appName := chi.URLParam(r, "app")

	var req rollbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if req.Environment == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"environment is required"})
		return
	}

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}
	envNs := constants.EnvNamespace(projectName, req.Environment)

	// Find the environment status.
	var envStatus *mortisev1alpha1.EnvironmentStatus
	for i := range app.Status.Environments {
		if app.Status.Environments[i].Name == req.Environment {
			envStatus = &app.Status.Environments[i]
			break
		}
	}
	if envStatus == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{fmt.Sprintf("environment %q not found in app status", req.Environment)})
		return
	}
	if req.Index < 0 || req.Index >= len(envStatus.DeployHistory) {
		writeJSON(w, http.StatusBadRequest, errorResponse{fmt.Sprintf("deploy history index %d out of range (len=%d)", req.Index, len(envStatus.DeployHistory))})
		return
	}

	target := envStatus.DeployHistory[req.Index]
	rollbackImage := target.Image
	if target.Digest != "" {
		rollbackImage = target.Digest
	}

	depName := fmt.Sprintf("%s-%s", appName, req.Environment)
	var dep appsv1.Deployment
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: depName, Namespace: envNs}, &dep); err != nil {
		writeError(w, err)
		return
	}
	if len(dep.Spec.Template.Spec.Containers) == 0 {
		writeJSON(w, http.StatusInternalServerError, errorResponse{fmt.Sprintf("deployment %s has no containers", depName)})
		return
	}

	dep.Spec.Template.Spec.Containers[0].Image = rollbackImage
	if err := s.client.Update(r.Context(), &dep); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, target)
}

type promoteRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Promote handles POST /api/projects/{p}/apps/{a}/promote.
// It reads the current image digest from the source environment's status and
// patches the target environment's Deployment with that image. A new
// DeployRecord is appended to the target environment's deploy history.
func (s *Server) Promote(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "app", Namespace: ns}, authz.ActionUpdate) {
		return
	}
	appName := chi.URLParam(r, "app")

	var req promoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if req.From == "" || req.To == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"from and to are required"})
		return
	}
	if req.From == req.To {
		writeJSON(w, http.StatusBadRequest, errorResponse{"from and to must be different environments"})
		return
	}

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	// Find source environment status.
	var fromStatus *mortisev1alpha1.EnvironmentStatus
	for i := range app.Status.Environments {
		if app.Status.Environments[i].Name == req.From {
			fromStatus = &app.Status.Environments[i]
			break
		}
	}
	if fromStatus == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{fmt.Sprintf("source environment %q not found in app status", req.From)})
		return
	}
	if fromStatus.CurrentImage == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{fmt.Sprintf("source environment %q has no current image", req.From)})
		return
	}

	// Verify the target environment exists in spec.
	targetFound := false
	for _, env := range app.Spec.Environments {
		if env.Name == req.To {
			targetFound = true
			break
		}
	}
	if !targetFound {
		writeJSON(w, http.StatusNotFound, errorResponse{fmt.Sprintf("target environment %q not found in app spec", req.To)})
		return
	}

	// Patch the target Deployment.
	promoteImage := fromStatus.CurrentImage
	if fromStatus.CurrentDigest != "" {
		promoteImage = fromStatus.CurrentDigest
	}

	depName := fmt.Sprintf("%s-%s", appName, req.To)
	toEnvNs := constants.EnvNamespace(projectName, req.To)
	var dep appsv1.Deployment
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: depName, Namespace: toEnvNs}, &dep); err != nil {
		writeError(w, err)
		return
	}
	if len(dep.Spec.Template.Spec.Containers) == 0 {
		writeJSON(w, http.StatusInternalServerError, errorResponse{fmt.Sprintf("deployment %s has no containers", depName)})
		return
	}

	dep.Spec.Template.Spec.Containers[0].Image = promoteImage
	if err := s.client.Update(r.Context(), &dep); err != nil {
		writeError(w, err)
		return
	}

	// Append a deploy record to the target environment's status.
	record := mortisev1alpha1.DeployRecord{
		Image:     fromStatus.CurrentImage,
		Digest:    fromStatus.CurrentDigest,
		Timestamp: metav1.Now(),
	}

	var toStatus *mortisev1alpha1.EnvironmentStatus
	for i := range app.Status.Environments {
		if app.Status.Environments[i].Name == req.To {
			toStatus = &app.Status.Environments[i]
			break
		}
	}
	if toStatus == nil {
		// Target env has no status yet; add one.
		app.Status.Environments = append(app.Status.Environments, mortisev1alpha1.EnvironmentStatus{
			Name: req.To,
		})
		toStatus = &app.Status.Environments[len(app.Status.Environments)-1]
	}
	toStatus.CurrentImage = fromStatus.CurrentImage
	toStatus.CurrentDigest = fromStatus.CurrentDigest
	toStatus.DeployHistory = append(toStatus.DeployHistory, record)

	if err := s.client.Status().Update(r.Context(), &app); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "promoted",
		"from":   req.From,
		"to":     req.To,
		"image":  promoteImage,
	})
}
