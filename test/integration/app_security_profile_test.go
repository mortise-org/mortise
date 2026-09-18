//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/test/helpers"
)

// An App with securityProfile: restricted must admit in a namespace that
// enforces the PodSecurity restricted profile (CAI-206). The namespace is
// created by the operator, so it is labelled once it exists and the pod is
// deleted to force re-admission under enforcement.
func TestRestrictedProfileAdmitsUnderPodSecurityEnforce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	projectName := "psa-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	_, thisFile, _, _ := runtime.Caller(0)
	app := helpers.LoadFixture(t, filepath.Join(filepath.Dir(thisFile), "..", "fixtures", "image-restricted.yaml"))
	app.Namespace = ns
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create App: %v", err)
	}
	envNs := constants.EnvNamespace(projectName, app.Spec.Environments[0].Name)

	podLabels := client.MatchingLabels{"app.kubernetes.io/name": app.Name}
	firstPod := ""
	helpers.RequireEventually(t, 120*time.Second, func() bool {
		var pods corev1.PodList
		if err := k8sClient.List(ctx, &pods, client.InNamespace(envNs), podLabels); err != nil || len(pods.Items) == 0 {
			return false
		}
		if pods.Items[0].Status.Phase != corev1.PodRunning {
			return false
		}
		firstPod = pods.Items[0].Name
		return true
	})

	var namespace corev1.Namespace
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: envNs}, &namespace); err != nil {
		t.Fatalf("get env namespace: %v", err)
	}
	if namespace.Labels == nil {
		namespace.Labels = map[string]string{}
	}
	namespace.Labels["pod-security.kubernetes.io/enforce"] = "restricted"
	if err := k8sClient.Update(ctx, &namespace); err != nil {
		t.Fatalf("label env namespace: %v", err)
	}

	if err := k8sClient.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(envNs), podLabels); err != nil {
		t.Fatalf("delete pod: %v", err)
	}

	// The ReplicaSet recreates the pod; under enforcement a non-compliant
	// template is rejected at admission and no pod ever appears.
	helpers.RequireEventually(t, 120*time.Second, func() bool {
		var pods corev1.PodList
		if err := k8sClient.List(ctx, &pods, client.InNamespace(envNs), podLabels); err != nil {
			return false
		}
		for _, p := range pods.Items {
			if p.Name != firstPod && p.Status.Phase == corev1.PodRunning {
				return true
			}
		}
		return false
	})
}
