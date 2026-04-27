package helpers

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// RequireEventually polls fn every 200ms until it returns true or timeout is reached.
func RequireEventually(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

// AssertPodsRunning asserts that a Deployment exists with the expected number of ready replicas.
func AssertPodsRunning(t *testing.T, k8sClient client.Client, ns, name string, count int32) {
	t.Helper()
	RequireEventually(t, 8*time.Minute, func() bool {
		var dep appsv1.Deployment
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      name,
			Namespace: ns,
		}, &dep)
		if err != nil {
			return false
		}
		return dep.Status.ReadyReplicas == count
	})
}

// AssertDeploymentExists asserts a Deployment with the given name exists in the namespace.
func AssertDeploymentExists(t *testing.T, k8sClient client.Client, ns, name string) {
	t.Helper()
	RequireEventually(t, 60*time.Second, func() bool {
		var dep appsv1.Deployment
		return k8sClient.Get(context.Background(), types.NamespacedName{
			Name: name, Namespace: ns,
		}, &dep) == nil
	})
}

// AssertIngressExists asserts an Ingress with the given name exists in the namespace.
func AssertIngressExists(t *testing.T, k8sClient client.Client, ns, name string) {
	t.Helper()
	RequireEventually(t, 60*time.Second, func() bool {
		var ing networkingv1.Ingress
		return k8sClient.Get(context.Background(), types.NamespacedName{
			Name: name, Namespace: ns,
		}, &ing) == nil
	})
}

// WaitForAppReady polls until App.status.phase == Ready or timeout is reached.
func WaitForAppReady(t *testing.T, k8sClient client.Client, ns, name string, timeout time.Duration) {
	t.Helper()
	RequireEventually(t, timeout, func() bool {
		var app mortisev1alpha1.App
		if err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: name, Namespace: ns,
		}, &app); err != nil {
			return false
		}
		return app.Status.Phase == mortisev1alpha1.AppPhaseReady
	})
}
