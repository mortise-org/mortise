//go:build integration

// These tests pin the shared-vars propagation contract (GH #351): env-set changes
// cascade via ProjectEnvsRevAnnotation and apps re-materialize shared-env from the
// control namespace. NOTE: they create fresh namespaces and wait on app readiness,
// so until the RBAC-propagation race (mo-k5p) is fixed, a timeout here is almost
// certainly that race — not a contract break.

package integration

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/envstore"
	"github.com/mortise-org/mortise/test/helpers"
)

// These tests prove the shared-vars propagation contract from the #351
// ruling: shared vars live in one control-namespace Secret and MUST reach
// every environment — including ones created after the vars were written,
// restricted ones, and live previews — without manual intervention.
//
// The propagation machinery under test:
//   - env added to Project.spec → project controller cascadeEnvChange bumps
//     ProjectEnvsRevAnnotation on every App → app reconcile materializes
//     shared-env into the new env namespace (no API poke involved);
//   - shared vars updated → the API pokes every App (PutSharedVars), and the
//     reconcile re-reads the control-ns source of truth for every resolved
//     env, previews included.

// appendProjectEnv adds an environment to the Project spec the same way the
// non-clone CreateProjectEnvironment API path does: a bare spec append, no
// app poke, no secret copy.
func appendProjectEnv(t *testing.T, projectName string, env mortisev1alpha1.ProjectEnvironment) {
	t.Helper()
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var project mortisev1alpha1.Project
		if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: projectName}, &project); err != nil {
			return err
		}
		project.Spec.Environments = append(project.Spec.Environments, env)
		return k8sClient.Update(context.Background(), &project)
	})
	if err != nil {
		t.Fatalf("append env %q to project %q: %v", env.Name, projectName, err)
	}
}

func sharedEnvValue(envNs, key string) func() bool {
	return func() bool {
		var secret corev1.Secret
		if err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: envstore.SharedEnvName, Namespace: envNs,
		}, &secret); err != nil {
			return false
		}
		return len(secret.Data[key]) > 0
	}
}

// TestSharedVarsReachNewEnv is the suspected-gap case: a shared var exists,
// then a NEW non-clone environment is added. Nothing else happens — no app
// poke, no var update. The new env's shared-env Secret must still
// materialize, purely via the project controller's env-change cascade.
func TestSharedVarsReachNewEnv(t *testing.T) {
	t.Parallel()
	projectName := "shared-newenv-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-basic.yaml"))
	app.Namespace = ns
	app.Name = "shared-app-" + randSuffix()
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create App: %v", err)
	}
	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 5*time.Minute)

	// Shared var exists BEFORE the env does (the ordering the contract
	// promises survives).
	store := &envstore.Store{Client: k8sClient}
	if err := store.MergeSharedSource(context.Background(), ns, []envstore.Env{
		{Name: "SHARED_BEFORE_ENV", Value: "present", Source: "shared"},
	}, nil); err != nil {
		t.Fatalf("seed shared source: %v", err)
	}

	newEnv := "staging2"
	appendProjectEnv(t, projectName, mortisev1alpha1.ProjectEnvironment{Name: newEnv})

	newEnvNs := constants.EnvNamespace(projectName, newEnv)
	helpers.RequireEventually(t, 2*time.Minute, sharedEnvValue(newEnvNs, "SHARED_BEFORE_ENV"))
}

// TestSharedVarsReachRestrictedEnv: the ruling's contract explicitly covers
// restricted environments — same cascade, restricted flag set.
func TestSharedVarsReachRestrictedEnv(t *testing.T) {
	t.Parallel()
	projectName := "shared-restricted-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-basic.yaml"))
	app.Namespace = ns
	app.Name = "restr-app-" + randSuffix()
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create App: %v", err)
	}
	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 5*time.Minute)

	store := &envstore.Store{Client: k8sClient}
	if err := store.MergeSharedSource(context.Background(), ns, []envstore.Env{
		{Name: "SHARED_RESTRICTED", Value: "present", Source: "shared"},
	}, nil); err != nil {
		t.Fatalf("seed shared source: %v", err)
	}

	restrictedEnv := "prod-locked"
	appendProjectEnv(t, projectName, mortisev1alpha1.ProjectEnvironment{
		Name:       restrictedEnv,
		Restricted: true,
	})

	restrictedNs := constants.EnvNamespace(projectName, restrictedEnv)
	helpers.RequireEventually(t, 2*time.Minute, sharedEnvValue(restrictedNs, "SHARED_RESTRICTED"))
}

// TestSharedVarsUpdateConvergesLiveEnvs: a shared-var update must converge
// into every ALREADY-materialized environment, including a live preview env.
// This exercises the PutSharedVars contract at the k8s level: write the
// control-ns source, then poke each app the way the API does. (Creation-time
// preview inheritance is covered by TestPreviewEnvironmentInheritsSourceEnvVars;
// here the preview env participates in the fan-out as a project env with
// preview=true, which is the state the PE controller leaves behind.)
func TestSharedVarsUpdateConvergesLiveEnvs(t *testing.T) {
	t.Parallel()
	projectName := "shared-converge-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-basic.yaml"))
	app.Namespace = ns
	app.Name = "conv-app-" + randSuffix()
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create App: %v", err)
	}
	prodEnv := app.Spec.Environments[0].Name
	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 5*time.Minute)

	previewEnv := "pr-4242"
	appendProjectEnv(t, projectName, mortisev1alpha1.ProjectEnvironment{
		Name:    previewEnv,
		Preview: true,
	})

	prodNs := constants.EnvNamespace(projectName, prodEnv)
	previewNs := constants.EnvNamespace(projectName, previewEnv)

	store := &envstore.Store{Client: k8sClient}
	if err := store.MergeSharedSource(context.Background(), ns, []envstore.Env{
		{Name: "ROLLOUT_FLAG", Value: "v1", Source: "shared"},
	}, nil); err != nil {
		t.Fatalf("seed shared source: %v", err)
	}
	pokeAllApps(t, ns)
	helpers.RequireEventually(t, 2*time.Minute, sharedEnvValue(prodNs, "ROLLOUT_FLAG"))
	helpers.RequireEventually(t, 2*time.Minute, sharedEnvValue(previewNs, "ROLLOUT_FLAG"))

	// Update the value; both live envs must converge to v2.
	if err := store.MergeSharedSource(context.Background(), ns, []envstore.Env{
		{Name: "ROLLOUT_FLAG", Value: "v2", Source: "shared"},
	}, nil); err != nil {
		t.Fatalf("update shared source: %v", err)
	}
	pokeAllApps(t, ns)

	converged := func(envNs string) func() bool {
		return func() bool {
			var secret corev1.Secret
			if err := k8sClient.Get(context.Background(), types.NamespacedName{
				Name: envstore.SharedEnvName, Namespace: envNs,
			}, &secret); err != nil {
				return false
			}
			return string(secret.Data["ROLLOUT_FLAG"]) == "v2"
		}
	}
	helpers.RequireEventually(t, 2*time.Minute, converged(prodNs))
	helpers.RequireEventually(t, 2*time.Minute, converged(previewNs))
}

// pokeAllApps replicates PutSharedVars' fan-out: bump the env-updated
// annotation on every App in the control namespace so their reconcilers
// re-read the shared source.
func pokeAllApps(t *testing.T, controlNs string) {
	t.Helper()
	var apps mortisev1alpha1.AppList
	if err := k8sClient.List(context.Background(), &apps, ctrlclient.InNamespace(controlNs)); err != nil {
		t.Fatalf("list apps: %v", err)
	}
	for i := range apps.Items {
		app := &apps.Items[i]
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var latest mortisev1alpha1.App
			if err := k8sClient.Get(context.Background(), types.NamespacedName{
				Namespace: app.Namespace, Name: app.Name,
			}, &latest); err != nil {
				return err
			}
			if latest.Annotations == nil {
				latest.Annotations = map[string]string{}
			}
			latest.Annotations["mortise.dev/env-updated"] = fmt.Sprintf("%d", time.Now().UnixNano())
			return k8sClient.Update(context.Background(), &latest)
		})
		if err != nil {
			t.Fatalf("poke app %q: %v", app.Name, err)
		}
	}
}
