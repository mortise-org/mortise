package helpers

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CreateTestNamespace creates a uniquely-named namespace and registers cleanup.
func CreateTestNamespace(t *testing.T, k8sClient client.Client) string {
	t.Helper()
	name := fmt.Sprintf("test-%s", rand.String(8))
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	if err := k8sClient.Create(context.Background(), ns); err != nil {
		t.Fatalf("failed to create test namespace: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), ns)
	})
	return name
}
