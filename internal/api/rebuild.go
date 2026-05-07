package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
)

// Rebuild triggers a fresh build from the latest git commit.
// It clears lastBuiltSHA so the reconciler treats the current revision as new,
// and resets the phase from Failed/CrashLooping so the build guard doesn't skip it.
//
// POST /api/projects/{project}/apps/{app}/rebuild
//
// @Summary Rebuild an app from source
// @Description Clear the last-built SHA and reset the phase so the reconciler triggers a fresh build. Only supported for git-source apps.
// @Tags deploy
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Success 200 {object} map[string]string
// @Failure 400 {object} errorResponse
// @Router /projects/{project}/apps/{app}/rebuild [post]
func (s *Server) Rebuild(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "app", Namespace: ns, Project: projectName}, authz.ActionUpdate) {
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

	// Signal a no-cache build so BuildKit rebuilds all layers from scratch.
	if app.Annotations == nil {
		app.Annotations = make(map[string]string)
	}
	app.Annotations["mortise.dev/no-cache-build"] = "true"
	if err := s.client.Update(r.Context(), &app); err != nil {
		writeError(w, err)
		return
	}

	// Re-fetch so the status update uses the current resourceVersion. A
	// controller reconcile between the annotation write and this point would
	// bump the resourceVersion, causing a stale-object conflict otherwise.
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
		writeError(w, err)
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

	s.recordActivity(r, projectName, "build", "app", appName, "Triggered rebuild for "+appName, "")

	writeJSON(w, http.StatusOK, map[string]string{"status": "rebuilding"})
}

// Redeploy triggers a rolling restart of an app's Deployment(s) by annotating
// the pod template. Works for any source type (git, image, external).
// This is the correct way to pick up Secret changes mounted via envFrom.
//
// POST /api/projects/{project}/apps/{app}/redeploy
//
// @Summary Redeploy an app (rolling restart)
// @Description Trigger a rolling restart by annotating the pod template. Works for any source type. Use this to pick up Secret changes.
// @Tags deploy
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Param environment query string false "Environment name (defaults to first env)"
// @Success 200 {object} map[string]string
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app}/redeploy [post]
func (s *Server) Redeploy(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionUpdate) {
		return
	}
	appName := chi.URLParam(r, "app")
	env := envFromQuery(r)

	envNs := constants.EnvNamespace(projectName, env)

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	pendingHash := envStatusPendingHash(app.Status.Environments, env)
	if err := restartDeployment(r.Context(), s.client, envNs, appName, pendingHash); err != nil {
		writeError(w, err)
		return
	}

	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	app.Status.Phase = mortisev1alpha1.AppPhaseDeploying
	for i := range app.Status.Environments {
		if app.Status.Environments[i].Name == env {
			app.Status.Environments[i].Phase = mortisev1alpha1.AppPhaseDeploying
			break
		}
	}
	if err := s.client.Status().Update(r.Context(), &app); err != nil {
		writeError(w, err)
		return
	}

	s.recordActivity(r, projectName, "deploy", "app", appName, "Triggered redeploy for "+appName+" in "+env, "")

	writeJSON(w, http.StatusOK, map[string]string{"status": "restarting"})
}

// RedeployStale triggers a rolling restart for every environment on an app
// that has unapplied env-var changes (PendingEnvHash != DeployedEnvHash).
//
// POST /api/projects/{project}/apps/{app}/redeploy-stale
//
// @Summary Redeploy all stale environments for an app
// @Description Triggers a rolling restart for every environment where env vars have changed but haven't been deployed yet
// @Tags deploy
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Success 200 {object} map[string][]string
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app}/redeploy-stale [post]
func (s *Server) RedeployStale(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionUpdate) {
		return
	}
	appName := chi.URLParam(r, "app")

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	var restarted []string
	for _, es := range app.Status.Environments {
		if es.PendingEnvHash == "" || es.DeployedEnvHash == "" || es.PendingEnvHash == es.DeployedEnvHash {
			continue
		}
		envNs := constants.EnvNamespace(projectName, es.Name)
		if err := restartDeployment(r.Context(), s.client, envNs, appName, es.PendingEnvHash); err != nil {
			writeError(w, err)
			return
		}
		restarted = append(restarted, es.Name)
	}

	if len(restarted) > 0 {
		if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
			writeError(w, err)
			return
		}
		app.Status.Phase = mortisev1alpha1.AppPhaseDeploying
		restartedSet := make(map[string]bool, len(restarted))
		for _, name := range restarted {
			restartedSet[name] = true
		}
		for i := range app.Status.Environments {
			if restartedSet[app.Status.Environments[i].Name] {
				app.Status.Environments[i].Phase = mortisev1alpha1.AppPhaseDeploying
			}
		}
		if err := s.client.Status().Update(r.Context(), &app); err != nil {
			writeError(w, err)
			return
		}
		s.recordActivity(r, projectName, "deploy", "app", appName, fmt.Sprintf("Redeployed stale envs for %s: %v", appName, restarted), "")
	}

	writeJSON(w, http.StatusOK, map[string][]string{"restarted": restarted})
}

func envStatusPendingHash(envStatuses []mortisev1alpha1.EnvironmentStatus, envName string) string {
	for _, es := range envStatuses {
		if es.Name == envName {
			return es.PendingEnvHash
		}
	}
	return ""
}

func restartDeployment(ctx context.Context, c client.Client, namespace, appName, pendingEnvHash string) error {
	var dep appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, &dep); err != nil {
		return err
	}

	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = make(map[string]string)
	}
	dep.Spec.Template.Annotations["mortise.dev/restartedAt"] = fmt.Sprintf("%d", time.Now().UnixMilli())
	if pendingEnvHash != "" {
		dep.Spec.Template.Annotations["mortise.dev/env-hash"] = pendingEnvHash
	}
	return c.Update(ctx, &dep)
}
