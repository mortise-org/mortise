package helpers

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

const testProjectDescription = "integration test"

func CreateTestProject(t *testing.T, k8sClient client.Client, name string) string {
	t.Helper()
	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       mortisev1alpha1.ProjectSpec{Description: testProjectDescription},
	}
	project.SetGroupVersionKind(mortisev1alpha1.GroupVersion.WithKind("Project"))

	if err := k8sClient.Create(context.Background(), project); err != nil {
		t.Fatalf("create project %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), project)
		RequireEventually(t, 60*time.Second, func() bool {
			var ns corev1.Namespace
			return k8sClient.Get(context.Background(), types.NamespacedName{Name: "pj-" + name}, &ns) != nil
		})
	})

	RequireEventually(t, 30*time.Second, func() bool {
		var p mortisev1alpha1.Project
		return k8sClient.Get(context.Background(), types.NamespacedName{Name: name}, &p) == nil &&
			p.Status.Phase == mortisev1alpha1.ProjectPhaseReady
	})
	return "pj-" + name
}
