package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
)

type rollbackRequest struct {
	Environment string `json:"environment"`
	Index       int    `json:"index"`
}

// Rollback handles POST /api/projects/{p}/apps/{a}/rollback.
// It reads the deploy history for the given environment, patches the
// Deployment back to the image at the specified history index, and returns
// the DeployRecord that was rolled back to.
//
// @Summary Rollback an app to a previous deploy
// @Description Roll back an app's environment to a previous image from its deploy history by index.
// @Tags rollback
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Param body body rollbackRequest true "Rollback details"
// @Success 200 {object} mortisev1alpha1.DeployRecord
// @Failure 400 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app}/rollback [post]
func (s *Server) Rollback(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
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
	if !s.authorize(w, r, authz.Resource{Kind: "app", Namespace: ns, Project: projectName, Environment: req.Environment}, authz.ActionUpdate) {
		return
	}

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
		writeError(w, r, err)
		return
	}
	if app.Spec.Source.Type == mortisev1alpha1.SourceTypeGit {
		writeJSON(w, http.StatusBadRequest, errorResponse{"rollback is not supported for git-source apps: the controller always deploys the freshly built image, so an image rollback would be silently reverted. Redeploy the target revision instead."})
		return
	}
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

	if err := s.setEnvSpecImage(r.Context(), projectName, appName, req.Environment, rollbackImage); err != nil {
		writeError(w, r, err)
		return
	}

	s.recordActivity(r, projectName, "rollback", "app", appName, fmt.Sprintf("Rolled back %s in %s", appName, req.Environment), "")

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
//
// @Summary Promote an app between environments
// @Description Copy the current image from one environment to another, patching the target Deployment and appending a deploy record.
// @Tags rollback
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Param body body promoteRequest true "Promote details"
// @Success 200 {object} map[string]string
// @Failure 400 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app}/promote [post]
func (s *Server) Promote(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
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
	// Authorize read on the source environment so developers cannot exfiltrate
	// production state by promoting FROM a restricted env to a less-restricted one.
	if !s.authorize(w, r, authz.Resource{Kind: "app", Namespace: ns, Project: projectName, Environment: req.From}, authz.ActionRead) {
		return
	}
	// Authorize update on the target environment (restricted-env guard).
	if !s.authorize(w, r, authz.Resource{Kind: "app", Namespace: ns, Project: projectName, Environment: req.To}, authz.ActionUpdate) {
		return
	}

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
		writeError(w, r, err)
		return
	}
	if app.Spec.Source.Type == mortisev1alpha1.SourceTypeGit {
		writeJSON(w, http.StatusBadRequest, errorResponse{"promote is not supported for git-source apps: the controller always deploys the target env's freshly built image, so a promoted image would be silently reverted."})
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

	if err := s.setEnvSpecImage(r.Context(), projectName, appName, req.To, promoteImage); err != nil {
		writeError(w, r, err)
		return
	}

	// Deploy history is owned by the App controller: setEnvSpecImage wrote the
	// target env's spec image, and the controller records the deploy — Confirmed
	// once it is actually running (CAI-501) — on reconcile, exactly as the deploy
	// handler relies on. Writing a record here too produced a duplicate,
	// mis-ordered, never-Confirmed entry (concurrency audit).

	s.recordActivity(r, projectName, "promote", "app", appName, fmt.Sprintf("Promoted %s from %s to %s", appName, req.From, req.To), "")

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "promoted",
		"from":   req.From,
		"to":     req.To,
		"image":  promoteImage,
	})
}

// setEnvSpecImage pins an env's image on the App spec so the controller rolls the
// Deployment to it and keeps it. Rollback and promote must NOT patch the
// Deployment directly: it is controller-owned, and reconcileDeployment reverts
// its image back to the spec/built image on the next reconcile, so a direct patch
// silently vanishes within seconds (CAI-498). This mirrors the deploy handler,
// which writes the spec. Only valid for image-source apps; git-source apps
// always use the freshly built image (see the git-source guard in the handlers).
func (s *Server) setEnvSpecImage(ctx context.Context, projectName, appName, envName, image string) error {
	appNs := constants.ControlNamespace(projectName)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var app mortisev1alpha1.App
		if err := s.client.Get(ctx, types.NamespacedName{Name: appName, Namespace: appNs}, &app); err != nil {
			return err
		}
		if envName != "" {
			ensureEnvironment(&app, envName).Image = image
		} else {
			app.Spec.Source.Image = image
		}
		return s.client.Update(ctx, &app)
	})
}

