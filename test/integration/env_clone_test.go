//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/envstore"
	"github.com/mortise-org/mortise/test/helpers"
)

// TestPreviewEnvironmentInheritsSourceEnvVars verifies that a preview
// environment inherits per-env vars and shared vars from its source
// environment, and that pe.Spec.Env overrides win.
func TestPreviewEnvironmentInheritsSourceEnvVars(t *testing.T) {
	t.Parallel()
	projectName := "prev-inherit-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	_, thisFile, _, _ := runtime.Caller(0)
	fixturesDir := filepath.Join(filepath.Dir(thisFile), "..", "fixtures")

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir, "image-basic.yaml"))
	app.Namespace = ns
	app.Name = "inherit-app-" + randSuffix()

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create App: %v", err)
	}

	envName := app.Spec.Environments[0].Name
	envNs := constants.EnvNamespace(projectName, envName)

	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 5*time.Minute)

	// Seed per-app env vars in the source environment.
	store := &envstore.Store{Client: k8sClient}
	if err := store.Merge(context.Background(), envNs, app.Name, []envstore.Env{
		{Name: "DATABASE_URL", Value: "postgres://prod:5432/app", Source: "user"},
		{Name: "LOG_LEVEL", Value: "info", Source: "user"},
	}, nil); err != nil {
		t.Fatalf("seed env vars: %v", err)
	}

	// Seed shared vars.
	if err := store.MergeShared(context.Background(), envNs, []envstore.Env{
		{Name: "SENTRY_DSN", Value: "https://sentry.io/123", Source: "shared"},
	}, nil); err != nil {
		t.Fatalf("seed shared vars: %v", err)
	}

	// Enable project-level preview.
	enableProjectPreview(t, projectName, &mortisev1alpha1.PreviewConfig{
		Enabled: true,
		Domain:  "pr-{number}-{app}.test.local",
		TTL:     "1h",
	})

	// Create a PreviewEnvironment against the source env.
	pe := &mortisev1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      app.Name + "-pr-100",
			Namespace: ns,
		},
		Spec: mortisev1alpha1.PreviewEnvironmentSpec{
			AppRef:    app.Name,
			SourceEnv: envName,
			PullRequest: mortisev1alpha1.PullRequestRef{
				Number: 100,
				Branch: "feat-test",
				SHA:    "abc123inherit",
			},
			Domain: "pr-100-" + app.Name + ".test.local",
			TTL:    metav1.Duration{Duration: 1 * time.Hour},
			Env: []mortisev1alpha1.EnvVar{
				{Name: "DATABASE_URL", Value: "postgres://preview:5432/test"},
			},
		},
	}
	pe.SetGroupVersionKind(mortisev1alpha1.GroupVersion.WithKind("PreviewEnvironment"))
	if err := k8sClient.Create(context.Background(), pe); err != nil {
		t.Fatalf("create PreviewEnvironment: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), pe) })

	previewNs := constants.PreviewNamespace(projectName, 100)

	// Wait for the preview env Secret to be created.
	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var secret corev1.Secret
		return k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      app.Name + "-env",
			Namespace: previewNs,
		}, &secret) == nil && len(secret.Data) > 0
	})

	// Read the preview app-env Secret.
	var envSecret corev1.Secret
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      app.Name + "-env",
		Namespace: previewNs,
	}, &envSecret); err != nil {
		t.Fatalf("get preview env Secret: %v", err)
	}

	envMap := make(map[string]string)
	for k, v := range envSecret.Data {
		envMap[k] = string(v)
	}

	// pe.Spec.Env override wins for DATABASE_URL.
	if got, want := envMap["DATABASE_URL"], "postgres://preview:5432/test"; got != want {
		t.Errorf("DATABASE_URL: got %q, want %q", got, want)
	}
	// Inherited per-env var preserved.
	if got, want := envMap["LOG_LEVEL"], "info"; got != want {
		t.Errorf("LOG_LEVEL: got %q, want %q", got, want)
	}

	// Verify shared-env was copied to preview namespace.
	var sharedSecret corev1.Secret
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      envstore.SharedEnvName,
		Namespace: previewNs,
	}, &sharedSecret); err != nil {
		t.Fatalf("get preview shared-env Secret: %v", err)
	}
	if got := string(sharedSecret.Data["SENTRY_DSN"]); got != "https://sentry.io/123" {
		t.Errorf("SENTRY_DSN in shared-env: got %q, want %q", got, "https://sentry.io/123")
	}
}

// TestEnvironmentCloneCreatesEnvWithConfig creates a project with production
// env + an app with env vars and bindings, then clones production to staging
// by directly modifying the Project and App CRDs (simulating what the clone
// API handler does), and verifies the controller reconciles the new env.
func TestEnvironmentCloneCreatesEnvWithConfig(t *testing.T) {
	t.Parallel()
	projectName := "clone-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	_, thisFile, _, _ := runtime.Caller(0)
	fixturesDir := filepath.Join(filepath.Dir(thisFile), "..", "fixtures")

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir, "image-basic.yaml"))
	app.Namespace = ns
	app.Name = "clone-app-" + randSuffix()

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create App: %v", err)
	}

	prodEnvName := app.Spec.Environments[0].Name
	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 5*time.Minute)

	// Seed production env vars.
	prodEnvNs := constants.EnvNamespace(projectName, prodEnvName)
	store := &envstore.Store{Client: k8sClient}
	if err := store.Merge(context.Background(), prodEnvNs, app.Name, []envstore.Env{
		{Name: "DATABASE_URL", Value: "postgres://prod:5432/app", Source: "user"},
		{Name: "API_KEY", Value: "prod-key-123", Source: "user"},
	}, nil); err != nil {
		t.Fatalf("seed env vars: %v", err)
	}

	// Add "staging" to the project environments.
	var project mortisev1alpha1.Project
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: projectName}, &project); err != nil {
		t.Fatalf("get project: %v", err)
	}
	project.Spec.Environments = append(project.Spec.Environments,
		mortisev1alpha1.ProjectEnvironment{Name: "staging", DisplayOrder: 1})
	if err := k8sClient.Update(context.Background(), &project); err != nil {
		t.Fatalf("add staging env: %v", err)
	}

	// Clone production's overrides to staging on the App.
	var latestApp mortisev1alpha1.App
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: app.Name}, &latestApp); err != nil {
		t.Fatalf("get app: %v", err)
	}
	var sourceEnv *mortisev1alpha1.Environment
	for i := range latestApp.Spec.Environments {
		if latestApp.Spec.Environments[i].Name == prodEnvName {
			sourceEnv = &latestApp.Spec.Environments[i]
			break
		}
	}
	clonedEnv := mortisev1alpha1.Environment{Name: "staging"}
	if sourceEnv != nil {
		clonedEnv.Replicas = sourceEnv.Replicas
		clonedEnv.Resources = sourceEnv.Resources
		if len(sourceEnv.Env) > 0 {
			clonedEnv.Env = make([]mortisev1alpha1.EnvVar, len(sourceEnv.Env))
			copy(clonedEnv.Env, sourceEnv.Env)
		}
		if len(sourceEnv.Bindings) > 0 {
			clonedEnv.Bindings = make([]mortisev1alpha1.Binding, len(sourceEnv.Bindings))
			copy(clonedEnv.Bindings, sourceEnv.Bindings)
		}
	}
	latestApp.Spec.Environments = append(latestApp.Spec.Environments, clonedEnv)
	if err := k8sClient.Update(context.Background(), &latestApp); err != nil {
		t.Fatalf("update app with staging env: %v", err)
	}

	// Wait for the staging env namespace and app-env Secret to appear.
	stagingEnvNs := constants.EnvNamespace(projectName, "staging")
	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var secret corev1.Secret
		return k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      app.Name + "-env",
			Namespace: stagingEnvNs,
		}, &secret) == nil
	})

	// Verify the cloned env vars seeded into the staging env Secret.
	var envSecret corev1.Secret
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      app.Name + "-env",
		Namespace: stagingEnvNs,
	}, &envSecret); err != nil {
		t.Fatalf("get staging env Secret: %v", err)
	}

	envMap := make(map[string]string)
	for k, v := range envSecret.Data {
		envMap[k] = string(v)
	}
	if got, want := envMap["DATABASE_URL"], "postgres://prod:5432/app"; got != want {
		t.Errorf("DATABASE_URL: got %q, want %q", got, want)
	}
	if got, want := envMap["API_KEY"], "prod-key-123"; got != want {
		t.Errorf("API_KEY: got %q, want %q", got, want)
	}
}
