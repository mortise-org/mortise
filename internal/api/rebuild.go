package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/git"
)

// Rebuild triggers a fresh build from the latest git commit.
// It resolves the current branch head, syncs mortise.dev/revision to that SHA,
// and records explicit rebuild request markers so the reconciler creates a new
// BuildRun even when the SHA is unchanged.
//
// POST /api/projects/{project}/apps/{app}/rebuild
//
// @Summary Rebuild an app from source
// @Description Resolve the current branch head, sync mortise.dev/revision to that SHA, and record explicit rebuild request markers so the reconciler triggers a fresh no-cache build. Only supported for git-source apps.
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
		writeError(w, r, err)
		return
	}

	if app.Spec.Source.Type != mortisev1alpha1.SourceTypeGit {
		writeJSON(w, http.StatusBadRequest, errorResponse{"rebuild is only supported for git-source apps"})
		return
	}

	revision, err := s.resolveGitBranchHead(r.Context(), &app)
	if err != nil {
		writeError(w, r, err)
		return
	}

	// Sync git state first, then record a new explicit request ID so the
	// controller creates a fresh durable BuildRun even for the same SHA.
	patch := client.MergeFrom(app.DeepCopy())
	requestedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if app.Annotations == nil {
		app.Annotations = make(map[string]string)
	}
	app.Annotations["mortise.dev/revision"] = revision
	app.Annotations["mortise.dev/rebuild-requested-at"] = requestedAt
	app.Annotations["mortise.dev/rebuild-no-cache-requested-at"] = requestedAt
	if err := s.client.Patch(r.Context(), &app, patch); err != nil {
		writeError(w, r, err)
		return
	}

	s.recordActivity(r, projectName, "build", "app", appName, "Triggered rebuild for "+appName, "")

	writeJSON(w, http.StatusOK, map[string]string{"status": "rebuilding"})
}

func (s *Server) resolveGitBranchHead(ctx context.Context, app *mortisev1alpha1.App) (string, error) {
	if app.Spec.Source.ProviderRef == "" {
		return "", fmt.Errorf("providerRef is required for git-source rebuilds")
	}

	var gp mortisev1alpha1.GitProvider
	if err := s.client.Get(ctx, types.NamespacedName{Name: app.Spec.Source.ProviderRef}, &gp); err != nil {
		return "", fmt.Errorf("get GitProvider %q: %w", app.Spec.Source.ProviderRef, err)
	}

	createdBy := app.Annotations["mortise.dev/created-by"]
	cachedOwner := app.Annotations["mortise.dev/git-token-owner"]
	tokenResult, err := git.ResolveGitTokenForApp(ctx, s.client, gp.Name, app.Namespace, createdBy, cachedOwner)
	if err != nil {
		return "", fmt.Errorf("resolve git token: %w", err)
	}

	apiFactory := s.GitAPIFactory
	if apiFactory == nil {
		apiFactory = git.NewGitAPIFromProvider
	}
	api, err := apiFactory(&gp, tokenResult.Token, "")
	if err != nil {
		return "", fmt.Errorf("create git API: %w", err)
	}

	branch := app.Spec.Source.Branch
	if branch == "" {
		branch = "main"
	}
	sha, err := api.ResolveBranchHead(ctx, app.Spec.Source.Repo, branch)
	if err != nil {
		return "", fmt.Errorf("resolve branch head: %w", err)
	}
	return sha, nil
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
// @Param environment query string false "Environment name (defaults to production)"
// @Success 200 {object} map[string]string
// @Failure 400 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app}/redeploy [post]
func (s *Server) Redeploy(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	appName := chi.URLParam(r, "app")
	env := envFromQuery(r)
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName, Environment: env}, authz.ActionUpdate) {
		return
	}

	app, env, ok := s.resolveDefaultedAppEnv(w, r)
	if !ok {
		return
	}
	envNs, err := envNamespace(app, env)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}

	pendingHash := envStatusPendingHash(app.Status.Environments, env)
	if err := restartDeployment(r.Context(), s.client, envNs, appName, pendingHash, s.clock().Now()); err != nil {
		writeError(w, r, err)
		return
	}

	if err := s.setEnvsDeploying(r.Context(), ns, appName, map[string]bool{env: true}); err != nil {
		writeError(w, r, err)
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
		writeError(w, r, err)
		return
	}

	p := PrincipalFromContext(r.Context())
	if p == nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{"authentication required"})
		return
	}

	var restarted []string
	var skipped []string
	for _, es := range app.Status.Environments {
		if es.PendingEnvHash == "" || es.DeployedEnvHash == "" || es.PendingEnvHash == es.DeployedEnvHash {
			continue
		}
		allowed, err := s.authz.Authorize(r.Context(), *p, authz.Resource{Kind: "app", Project: projectName, Environment: es.Name}, authz.ActionUpdate)
		if err != nil {
			writeError(w, r, err)
			return
		}
		if !allowed {
			skipped = append(skipped, es.Name)
			continue
		}
		envNs := constants.EnvNamespace(projectName, es.Name)
		if err := restartDeployment(r.Context(), s.client, envNs, appName, es.PendingEnvHash, s.clock().Now()); err != nil {
			writeError(w, r, err)
			return
		}
		restarted = append(restarted, es.Name)
	}

	if len(restarted) > 0 {
		restartedSet := make(map[string]bool, len(restarted))
		for _, name := range restarted {
			restartedSet[name] = true
		}
		if err := s.setEnvsDeploying(r.Context(), ns, appName, restartedSet); err != nil {
			writeError(w, r, err)
			return
		}
		s.recordActivity(r, projectName, "deploy", "app", appName, fmt.Sprintf("Redeployed stale envs for %s: %v", appName, restarted), "")
	}

	resp := map[string][]string{"restarted": restarted}
	if len(skipped) > 0 {
		resp["skipped"] = skipped
	}
	writeJSON(w, http.StatusOK, resp)
}

func envStatusPendingHash(envStatuses []mortisev1alpha1.EnvironmentStatus, envName string) string {
	for _, es := range envStatuses {
		if es.Name == envName {
			return es.PendingEnvHash
		}
	}
	return ""
}

// restartDeployment stamps the rollout annotations that trigger a redeploy.
// The app controller concurrently writes env-hash/rollout stamps to the same
// Deployment, so the Get→mutate→Update runs inside RetryOnConflict — otherwise
// a controller write between our Get and Update surfaces as a user-facing 409.
func restartDeployment(ctx context.Context, c client.Client, namespace, appName, pendingEnvHash string, now time.Time) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var dep appsv1.Deployment
		if err := c.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, &dep); err != nil {
			return err
		}

		if dep.Spec.Template.Annotations == nil {
			dep.Spec.Template.Annotations = make(map[string]string)
		}
		dep.Spec.Template.Annotations["mortise.dev/restartedAt"] = fmt.Sprintf("%d", now.UnixMilli())
		if pendingEnvHash != "" {
			dep.Spec.Template.Annotations["mortise.dev/env-hash"] = pendingEnvHash
		}
		return c.Update(ctx, &dep)
	})
}

// setEnvsDeploying marks the App and the named environments as Deploying,
// re-reading inside a conflict-retry loop so a concurrent controller status
// write doesn't surface as a user-facing 409.
func (s *Server) setEnvsDeploying(ctx context.Context, ns, appName string, envs map[string]bool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var app mortisev1alpha1.App
		if err := s.client.Get(ctx, types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
			return err
		}
		app.Status.Phase = mortisev1alpha1.AppPhaseDeploying
		for i := range app.Status.Environments {
			if envs[app.Status.Environments[i].Name] {
				app.Status.Environments[i].Phase = mortisev1alpha1.AppPhaseDeploying
			}
		}
		return s.client.Status().Update(ctx, &app)
	})
}
