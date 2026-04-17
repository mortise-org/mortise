//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/test/helpers"
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
		waitForNamespaceGone(t, "project-"+name)
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
	nsName := "project-" + name
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

func TestProjectNamespaceOverride(t *testing.T) {
	name := "proj-override-" + randSuffix()
	customNS := "custom-" + randSuffix()
	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: mortisev1alpha1.ProjectSpec{
			Description:       "override test",
			NamespaceOverride: customNS,
		},
	}
	project.SetGroupVersionKind(mortisev1alpha1.GroupVersion.WithKind("Project"))

	if err := k8sClient.Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), project)
		waitForNamespaceGone(t, customNS)
	})

	helpers.RequireEventually(t, 30*time.Second, func() bool {
		var p mortisev1alpha1.Project
		if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: name}, &p); err != nil {
			return false
		}
		return p.Status.Phase == mortisev1alpha1.ProjectPhaseReady
	})

	// Verify the custom namespace exists (not the default project-{name}).
	var ns corev1.Namespace
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: customNS}, &ns); err != nil {
		t.Fatalf("custom namespace %s not found: %v", customNS, err)
	}

	// Verify the default namespace does NOT exist.
	var defaultNS corev1.Namespace
	err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "project-" + name}, &defaultNS)
	if err == nil {
		t.Errorf("default namespace project-%s should not exist when override is set", name)
	}
}

func TestProjectAdoptExistingNamespace(t *testing.T) {
	name := "proj-adopt-" + randSuffix()
	nsName := "project-" + name

	// Pre-create the namespace without Mortise labels.
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: nsName},
	}
	if err := k8sClient.Create(context.Background(), ns); err != nil {
		t.Fatalf("pre-create namespace: %v", err)
	}

	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: mortisev1alpha1.ProjectSpec{
			Description:           "adoption test",
			AdoptExistingNamespace: true,
		},
	}
	project.SetGroupVersionKind(mortisev1alpha1.GroupVersion.WithKind("Project"))

	if err := k8sClient.Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), project)
		waitForNamespaceGone(t, nsName)
	})

	helpers.RequireEventually(t, 30*time.Second, func() bool {
		var p mortisev1alpha1.Project
		if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: name}, &p); err != nil {
			return false
		}
		return p.Status.Phase == mortisev1alpha1.ProjectPhaseReady
	})

	// Verify namespace got Mortise ownership labels.
	var updated corev1.Namespace
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: nsName}, &updated); err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if updated.Labels["app.kubernetes.io/managed-by"] != "mortise" {
		t.Errorf("expected managed-by=mortise after adoption, got %q", updated.Labels["app.kubernetes.io/managed-by"])
	}
	if updated.Labels["mortise.dev/project"] != name {
		t.Errorf("expected mortise.dev/project=%s after adoption, got %q", name, updated.Labels["mortise.dev/project"])
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

	nsName := "project-" + name

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
	resourceName := app.Name + "-" + envName
	helpers.AssertDeploymentExists(t, k8sClient, nsName, resourceName)

	// Delete the Project.
	if err := k8sClient.Delete(context.Background(), project); err != nil {
		t.Fatalf("delete project: %v", err)
	}

	// Wait for namespace to be gone (cascade).
	waitForNamespaceGone(t, nsName)

	// Verify app resources are gone.
	var dep appsv1.Deployment
	err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: resourceName, Namespace: nsName,
	}, &dep)
	if err == nil {
		t.Error("expected deployment to be gone after project deletion")
	}
}

func TestProjectNamespaceConflict(t *testing.T) {
	sharedNS := "conflict-" + randSuffix()

	// First project claims the namespace via override.
	p1Name := "proj-conflict1-" + randSuffix()
	p1 := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: p1Name},
		Spec: mortisev1alpha1.ProjectSpec{
			Description:       "conflict first",
			NamespaceOverride: sharedNS,
		},
	}
	p1.SetGroupVersionKind(mortisev1alpha1.GroupVersion.WithKind("Project"))

	if err := k8sClient.Create(context.Background(), p1); err != nil {
		t.Fatalf("create project 1: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), p1)
		waitForNamespaceGone(t, sharedNS)
	})

	helpers.RequireEventually(t, 30*time.Second, func() bool {
		var p mortisev1alpha1.Project
		if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: p1Name}, &p); err != nil {
			return false
		}
		return p.Status.Phase == mortisev1alpha1.ProjectPhaseReady
	})

	// Second project tries the same namespace.
	p2Name := "proj-conflict2-" + randSuffix()
	p2 := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: p2Name},
		Spec: mortisev1alpha1.ProjectSpec{
			Description:       "conflict second",
			NamespaceOverride: sharedNS,
		},
	}
	p2.SetGroupVersionKind(mortisev1alpha1.GroupVersion.WithKind("Project"))

	if err := k8sClient.Create(context.Background(), p2); err != nil {
		t.Fatalf("create project 2: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), p2)
	})

	// Second project should fail with a NamespaceConflict condition.
	helpers.RequireEventually(t, 30*time.Second, func() bool {
		var p mortisev1alpha1.Project
		if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: p2Name}, &p); err != nil {
			return false
		}
		if p.Status.Phase != mortisev1alpha1.ProjectPhaseFailed {
			return false
		}
		cond := meta.FindStatusCondition(p.Status.Conditions, "NamespaceReady")
		return cond != nil && cond.Reason == "NamespaceConflict"
	})
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
