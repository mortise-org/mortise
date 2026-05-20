package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	if err := s.rollbackDeployment(r.Context(), projectName, appName, req.Environment, rollbackImage); err != nil {
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

	if err := s.promoteDeployment(r.Context(), projectName, appName, req.To, promoteImage); err != nil {
		writeError(w, r, err)
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

	if err := s.recordPromotedDeploy(r.Context(), ns, appName, req.From, req.To); err != nil {
		writeError(w, r, err)
		return
	}

	s.recordActivity(r, projectName, "promote", "app", appName, fmt.Sprintf("Promoted %s from %s to %s", appName, req.From, req.To), "")

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "promoted",
		"from":   req.From,
		"to":     req.To,
		"image":  promoteImage,
	})
}

func (s *Server) rollbackDeployment(ctx context.Context, projectName, appName, envName, image string) error {
	depName := constants.DeploymentName(appName)
	envNs := constants.EnvNamespace(projectName, envName)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var dep appsv1.Deployment
		if err := s.client.Get(ctx, types.NamespacedName{Name: depName, Namespace: envNs}, &dep); err != nil {
			return err
		}
		if len(dep.Spec.Template.Spec.Containers) == 0 {
			return fmt.Errorf("deployment %s has no containers", depName)
		}
		dep.Spec.Template.Spec.Containers[0].Image = image
		return s.client.Update(ctx, &dep)
	})
}

func (s *Server) promoteDeployment(ctx context.Context, projectName, appName, envName, image string) error {
	depName := constants.DeploymentName(appName)
	envNs := constants.EnvNamespace(projectName, envName)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var dep appsv1.Deployment
		if err := s.client.Get(ctx, types.NamespacedName{Name: depName, Namespace: envNs}, &dep); err != nil {
			return err
		}
		if len(dep.Spec.Template.Spec.Containers) == 0 {
			return fmt.Errorf("deployment %s has no containers", depName)
		}
		dep.Spec.Template.Spec.Containers[0].Image = image
		return s.client.Update(ctx, &dep)
	})
}

func (s *Server) recordPromotedDeploy(ctx context.Context, ns, appName, fromEnv, toEnv string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var app mortisev1alpha1.App
		if err := s.client.Get(ctx, types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
			return err
		}

		var fromStatus *mortisev1alpha1.EnvironmentStatus
		for i := range app.Status.Environments {
			if app.Status.Environments[i].Name == fromEnv {
				fromStatus = &app.Status.Environments[i]
				break
			}
		}
		if fromStatus == nil {
			return fmt.Errorf("source environment %q not found in app status", fromEnv)
		}

		record := mortisev1alpha1.DeployRecord{
			Image:     fromStatus.CurrentImage,
			Digest:    fromStatus.CurrentDigest,
			Timestamp: metav1.Now(),
		}

		var toStatus *mortisev1alpha1.EnvironmentStatus
		for i := range app.Status.Environments {
			if app.Status.Environments[i].Name == toEnv {
				toStatus = &app.Status.Environments[i]
				break
			}
		}
		if toStatus == nil {
			app.Status.Environments = append(app.Status.Environments, mortisev1alpha1.EnvironmentStatus{
				Name: toEnv,
			})
			toStatus = &app.Status.Environments[len(app.Status.Environments)-1]
		}
		toStatus.CurrentImage = fromStatus.CurrentImage
		toStatus.CurrentDigest = fromStatus.CurrentDigest
		toStatus.DeployHistory = append(toStatus.DeployHistory, record)

		return s.client.Status().Update(ctx, &app)
	})
}
