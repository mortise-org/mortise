package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestLogsNonexistentApp(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/no-such-app/logs", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] == "" {
		t.Error("expected error message in response")
	}
}

func TestLogsNonexistentProject(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/projects/ghost/apps/anything/logs", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for logs in nonexistent project, got %d: %s", w.Code, w.Body.String())
	}
}

func TestLogsNoPods(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "logs-app",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/logs-app/logs", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for no pods, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] == "" {
		t.Error("expected error message about no pods")
	}
}

func TestLogsErrorResponseIsJSON(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "headers-app",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/headers-app/logs", nil)
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json for error response, got %s", ct)
	}
}

// TestLogsAcceptsTailAndEnvParams exercises the tail + env query parsing
// without needing real pod streaming. Uses a nonexistent env so the handler
// 404s after the parsing step.
func TestLogsAcceptsTailAndEnvParams(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "params-app",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/params-app/logs?tail=50&env=production", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for no pods, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] == "" {
		t.Error("expected error message")
	}
}

// TestLogsSSEHeadersWithPod verifies SSE headers and event structure when a
// matching Pod object exists. The fake clientset returns synthetic log output
// regardless of the pod's real runtime state.
func TestLogsSSEHeadersWithPod(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	nsName := seedProject(t, k8sClient, "default")

	doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "stream-app",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stream-app-pod-1",
			Namespace: nsName,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "stream-app",
				"app.kubernetes.io/managed-by": "mortise",
				"mortise.dev/environment":      "production",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25.0"}},
		},
	}
	if err := k8sClient.Create(context.Background(), pod); err != nil {
		t.Fatalf("create pod: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/stream-app/logs?env=production&tail=10", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %s", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("expected Cache-Control no-cache, got %s", cc)
	}

	body := w.Body.String()
	if !strings.HasPrefix(body, "data: ") {
		t.Fatalf("expected SSE body to start with 'data: ', got: %q", body)
	}

	line := strings.TrimPrefix(body, "data: ")
	if idx := strings.Index(line, "\n"); idx > 0 {
		line = line[:idx]
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("parse SSE event JSON: %v (line=%q)", err, line)
	}
	if ev["pod"] != "stream-app-pod-1" {
		t.Errorf("expected pod field %q, got %v", "stream-app-pod-1", ev["pod"])
	}
	if _, ok := ev["line"]; !ok {
		t.Errorf("expected line field in SSE event, got %v", ev)
	}
	if _, ok := ev["stream"]; !ok {
		t.Errorf("expected stream field in SSE event, got %v", ev)
	}
}

// TestLogsAcceptsTokenQueryParam verifies that the /logs endpoint treats
// ?token=<jwt> as a fallback when the Authorization header is absent
// (EventSource workaround).
func TestLogsAcceptsTokenQueryParam(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, token := newTestServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "tok-app",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	path := "/api/projects/default/apps/tok-app/logs?token=" + token
	w := doRequestWithToken(h, http.MethodGet, path, nil, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 (auth passed, no pods), got %d: %s", w.Code, w.Body.String())
	}
}

// TestLogsRequiresAuth verifies that /logs still requires authentication —
// the query-param token fallback doesn't weaken the auth requirement.
func TestLogsRequiresAuth(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodGet, "/api/projects/default/apps/anything/logs", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", w.Code)
	}
}
