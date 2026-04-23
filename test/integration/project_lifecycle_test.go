//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/test/helpers"
)

func TestProjectCreatesNamespace(t *testing.T) {
	name := "proj-ns-" + randSuffix()
	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       mortisev1alpha1.ProjectSpec{Description: "test"},
	}
	project.SetGroupVersionKind(mortisev1alpha1.GroupVersion.WithKind("Project"))

	if err := k8sClient.Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), project)
		waitForNamespaceGone(t, "pj-"+name)
	})

	// Wait for status.phase=Ready.
	helpers.RequireEventually(t, 30*time.Second, func() bool {
		var p mortisev1alpha1.Project
		if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: name}, &p); err != nil {
			return false
		}
		return p.Status.Phase == mortisev1alpha1.ProjectPhaseReady
	})

	// Verify namespace exists with correct labels.
	nsName := "pj-" + name
	var ns corev1.Namespace
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: nsName}, &ns); err != nil {
		t.Fatalf("namespace %s not found: %v", nsName, err)
	}
	if ns.Labels["app.kubernetes.io/managed-by"] != "mortise" {
		t.Errorf("expected managed-by=mortise, got %q", ns.Labels["app.kubernetes.io/managed-by"])
	}
	if ns.Labels["mortise.dev/project"] != name {
		t.Errorf("expected mortise.dev/project=%s, got %q", name, ns.Labels["mortise.dev/project"])
	}
}

func TestProjectDeleteCascades(t *testing.T) {
	name := "proj-cascade-" + randSuffix()
	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       mortisev1alpha1.ProjectSpec{Description: "cascade test"},
	}
	project.SetGroupVersionKind(mortisev1alpha1.GroupVersion.WithKind("Project"))

	if err := k8sClient.Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	nsName := "pj-" + name

	// Wait for Ready.
	helpers.RequireEventually(t, 30*time.Second, func() bool {
		var p mortisev1alpha1.Project
		if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: name}, &p); err != nil {
			return false
		}
		return p.Status.Phase == mortisev1alpha1.ProjectPhaseReady
	})

	// Create an App inside the project namespace.
	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-basic.yaml"))
	app.Namespace = nsName
	app.Name = "cascade-app"
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	// Wait for the App's Deployment to exist.
	envName := app.Spec.Environments[0].Name
	envNs := constants.EnvNamespace(name, envName)
	resourceName := app.Name
	helpers.AssertDeploymentExists(t, k8sClient, envNs, resourceName)

	// Delete the Project.
	if err := k8sClient.Delete(context.Background(), project); err != nil {
		t.Fatalf("delete project: %v", err)
	}

	// Wait for namespace to be gone (cascade).
	waitForNamespaceGone(t, nsName)

	// Verify app resources are gone.
	var dep appsv1.Deployment
	err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: resourceName, Namespace: envNs,
	}, &dep)
	if err == nil {
		t.Error("expected deployment to be gone after project deletion")
	}
}

// waitForNamespaceGone polls until the namespace no longer exists.
func waitForNamespaceGone(t *testing.T, name string) {
	t.Helper()
	helpers.RequireEventually(t, 90*time.Second, func() bool {
		var ns corev1.Namespace
		err := k8sClient.Get(context.Background(), types.NamespacedName{Name: name}, &ns)
		return err != nil
	})
}
