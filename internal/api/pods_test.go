package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/api"
	"github.com/mortise-org/mortise/internal/constants"
)

func TestHandlePodsNonexistentProject(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/projects/ghost/apps/x/pods", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandlePodsNonexistentApp(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/no-such/pods", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing app, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandlePodsReturnsEmptyArray(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	// App exists, no pods.
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "empty-app", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/empty-app/pods", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Must be an empty JSON array, not null.
	body := w.Body.String()
	if body != "[]\n" && body != "[]" {
		t.Errorf("expected [] body, got %q", body)
	}
	var out []map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected 0 entries, got %d", len(out))
	}
}

func TestHandlePodsReturnsSummaries(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")
	envNs := constants.EnvNamespace("default", "production")
	ctx := context.Background()

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "sum-app", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	labels := map[string]string{
		"app.kubernetes.io/name":       "sum-app",
		"app.kubernetes.io/managed-by": "mortise",
		"mortise.dev/environment":      "production",
	}
	for _, name := range []string{"sum-app-a", "sum-app-b"} {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: envNs,
				Labels:    labels,
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25.0"}},
			},
		}
		if err := k8sClient.Create(ctx, pod); err != nil {
			t.Fatalf("create pod %s: %v", name, err)
		}
		// Status must be written via the status subresource.
		pod.Status = corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", RestartCount: 2, State: corev1.ContainerState{
					Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(time.Now().Add(-5 * time.Minute))},
				}},
			},
		}
		if err := k8sClient.Status().Update(ctx, pod); err != nil {
			t.Fatalf("update pod status %s: %v", name, err)
		}
	}

	// Off-env pod must not appear in the production query.
	offEnv := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sum-app-staging",
			Namespace: envNs,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "sum-app",
				"app.kubernetes.io/managed-by": "mortise",
				"mortise.dev/environment":      "staging",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25.0"}},
		},
	}
	if err := k8sClient.Create(ctx, offEnv); err != nil {
		t.Fatalf("create off-env pod: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/sum-app/pods?env=production", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 pod summaries, got %d (body=%s)", len(out), w.Body.String())
	}
	for _, p := range out {
		if p["phase"] != "Running" {
			t.Errorf("expected phase Running, got %v", p["phase"])
		}
		if p["ready"] != true {
			t.Errorf("expected ready=true, got %v", p["ready"])
		}
		if rc, ok := p["restartCount"].(float64); !ok || int(rc) != 2 {
			t.Errorf("expected restartCount=2, got %v", p["restartCount"])
		}
		if p["createdAt"] == "" || p["createdAt"] == nil {
			t.Errorf("expected createdAt to be populated, got %v", p["createdAt"])
		}
		if p["startedAt"] == nil || p["startedAt"] == "" {
			t.Errorf("expected startedAt to be set for running container, got %v", p["startedAt"])
		}
	}
}

// TestHandlePodsRequiresAuth: /pods sits under the authenticated group, so
// unauthenticated requests must 401.
func TestHandlePodsRequiresAuth(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodGet, "/api/projects/default/apps/x/pods", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", w.Code)
	}
}

// TestSummarizePodNotRunning exercises the branch where no container is
// running — StartedAt stays empty, ready=false, restart count still sums.
func TestSummarizePodNotRunning(t *testing.T) {
	now := metav1.Now()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "waiting", CreationTimestamp: now},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "a", RestartCount: 1, State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
				}},
				{Name: "b", RestartCount: 3, State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
				}},
			},
		},
	}
	s := api.SummarizePodForTest(pod)
	if s.Ready {
		t.Error("expected Ready=false when no PodReady condition set")
	}
	if s.RestartCount != 4 {
		t.Errorf("expected RestartCount=4, got %d", s.RestartCount)
	}
	if s.StartedAt != "" {
		t.Errorf("expected empty StartedAt for non-running pod, got %q", s.StartedAt)
	}
	if s.CreatedAt == "" {
		t.Error("expected CreatedAt to be populated from pod.CreationTimestamp")
	}
}
