package api

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/mortise-org/mortise/internal/constants"
)

func TestFindAppPodPrefersRunningPod(t *testing.T) {
	ns := "pj-default-production"
	appName := "web"
	env := "production"

	cs := fake.NewClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "web-pending",
				Namespace: ns,
				Labels: map[string]string{
					constants.AppNameLabel:         appName,
					constants.EnvironmentLabel:     env,
					"app.kubernetes.io/managed-by": "mortise",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25.0"}},
			},
			Status: corev1.PodStatus{Phase: corev1.PodPending},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "web-running",
				Namespace: ns,
				Labels: map[string]string{
					constants.AppNameLabel:         appName,
					constants.EnvironmentLabel:     env,
					"app.kubernetes.io/managed-by": "mortise",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25.0"}},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)

	srv := &Server{clientset: cs}
	podName, containerName, err := srv.findAppPod(context.Background(), ns, appName, env)
	if err != nil {
		t.Fatalf("findAppPod: %v", err)
	}
	if podName != "web-running" {
		t.Fatalf("expected running pod, got %q", podName)
	}
	if containerName != "app" {
		t.Fatalf("expected first app container, got %q", containerName)
	}
}

func TestFindAppPodSkipsTerminatingAndWrongPods(t *testing.T) {
	ns := "pj-default-production"
	appName := "web"
	env := "production"
	now := metav1.NewTime(time.Now())

	cs := fake.NewClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "web-terminating",
				Namespace: ns,
				Labels: map[string]string{
					constants.AppNameLabel:         appName,
					constants.EnvironmentLabel:     env,
					"app.kubernetes.io/managed-by": "mortise",
				},
				DeletionTimestamp: &now,
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25.0"}},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "foreign-running",
				Namespace: ns,
				Labels: map[string]string{
					constants.AppNameLabel:         "other",
					constants.EnvironmentLabel:     env,
					"app.kubernetes.io/managed-by": "mortise",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25.0"}},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "web-running",
				Namespace: ns,
				Labels: map[string]string{
					constants.AppNameLabel:         appName,
					constants.EnvironmentLabel:     env,
					"app.kubernetes.io/managed-by": "mortise",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25.0"}},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)

	srv := &Server{clientset: cs}
	podName, containerName, err := srv.findAppPod(context.Background(), ns, appName, env)
	if err != nil {
		t.Fatalf("findAppPod: %v", err)
	}
	if podName != "web-running" {
		t.Fatalf("expected non-terminating matching pod, got %q", podName)
	}
	if containerName != "app" {
		t.Fatalf("expected first app container, got %q", containerName)
	}
}

func TestFindAppPodTargetsAppNamedContainerWhenSidecarComesFirst(t *testing.T) {
	ns := "pj-default-production"
	appName := "web"
	env := "production"

	cs := fake.NewClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "web-running",
				Namespace: ns,
				Labels: map[string]string{
					constants.AppNameLabel:         appName,
					constants.EnvironmentLabel:     env,
					"app.kubernetes.io/managed-by": "mortise",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "istio-proxy", Image: "proxy:latest"},
					{Name: appName, Image: "nginx:1.25.0"},
				},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)

	srv := &Server{clientset: cs}
	podName, containerName, err := srv.findAppPod(context.Background(), ns, appName, env)
	if err != nil {
		t.Fatalf("findAppPod: %v", err)
	}
	if podName != "web-running" {
		t.Fatalf("expected running pod, got %q", podName)
	}
	if containerName != appName {
		t.Fatalf("expected app container %q, got %q", appName, containerName)
	}
}

func TestFindAppPodErrorsWhenNoRunningMatchingPodsRemain(t *testing.T) {
	ns := "pj-default-production"
	appName := "web"
	env := "production"
	now := metav1.NewTime(time.Now())

	cs := fake.NewClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "web-pending",
				Namespace: ns,
				Labels: map[string]string{
					constants.AppNameLabel:         appName,
					constants.EnvironmentLabel:     env,
					"app.kubernetes.io/managed-by": "mortise",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25.0"}},
			},
			Status: corev1.PodStatus{Phase: corev1.PodPending},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "web-terminating",
				Namespace: ns,
				Labels: map[string]string{
					constants.AppNameLabel:         appName,
					constants.EnvironmentLabel:     env,
					"app.kubernetes.io/managed-by": "mortise",
				},
				DeletionTimestamp: &now,
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25.0"}},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)

	srv := &Server{clientset: cs}
	if _, _, err := srv.findAppPod(context.Background(), ns, appName, env); err == nil {
		t.Fatal("expected error when no running matching pod remains")
	}
}

func TestFindAppPodReturnsLookupErrorOnListFailure(t *testing.T) {
	cs := fake.NewClientset()
	cs.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("boom")
	})

	srv := &Server{clientset: cs}
	if _, _, err := srv.findAppPod(context.Background(), "pj-default-production", "web", "production"); !errors.Is(err, errExecPodLookup) {
		t.Fatalf("expected pod lookup error, got %v", err)
	}
}
