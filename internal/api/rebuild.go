package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	if err := restartDeployment(r.Context(), s.client, envNs, appName); err != nil {
		writeError(w, err)
		return
	}

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	var envStatus *mortisev1alpha1.EnvironmentStatus
	for i := range app.Status.Environments {
		if app.Status.Environments[i].Name == env {
			envStatus = &app.Status.Environments[i]
			break
		}
	}
	if envStatus != nil && envStatus.CurrentImage != "" {
		record := mortisev1alpha1.DeployRecord{
			Image:     envStatus.CurrentImage,
			Digest:    envStatus.CurrentDigest,
			Timestamp: metav1.Now(),
		}
		envStatus.DeployHistory = append([]mortisev1alpha1.DeployRecord{record}, envStatus.DeployHistory...)
		if len(envStatus.DeployHistory) > 20 {
			envStatus.DeployHistory = envStatus.DeployHistory[:20]
		}
		_ = s.client.Status().Update(r.Context(), &app)
	}

	s.recordActivity(r, projectName, "deploy", "app", appName, "Triggered redeploy for "+appName+" in "+env, "")

	writeJSON(w, http.StatusOK, map[string]string{"status": "restarting"})
}

func restartDeployment(ctx context.Context, c client.Client, namespace, appName string) error {
	var dep appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, &dep); err != nil {
		return err
	}

	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = make(map[string]string)
	}
	dep.Spec.Template.Annotations["mortise.dev/restartedAt"] = fmt.Sprintf("%d", time.Now().UnixMilli())
	return c.Update(ctx, &dep)
}
