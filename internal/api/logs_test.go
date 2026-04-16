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
	srv := oldNewServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/apps/no-such-app/logs?namespace=default", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] == "" {
		t.Error("expected error message in response")
	}
}

func TestLogsNoPods(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := oldNewServer(t, k8sClient)
	h := srv.Handler()

	// Create an app so it exists, but no pods will be present.
	doRequest(h, http.MethodPost, "/api/apps", map[string]any{
		"name":      "logs-app",
		"namespace": "default",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	w := doRequest(h, http.MethodGet, "/api/apps/logs-app/logs?namespace=default", nil)
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
	srv := oldNewServer(t, k8sClient)
	h := srv.Handler()

	doRequest(h, http.MethodPost, "/api/apps", map[string]any{
		"name":      "headers-app",
		"namespace": "default",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	w := doRequest(h, http.MethodGet, "/api/apps/headers-app/logs?namespace=default", nil)
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json for error response, got %s", ct)
	}
}

// TestLogsAcceptsTailAndEnvParams verifies the endpoint parses tail and env
// query params without erroring. Uses a nonexistent env so it 404s without
// needing to actually stream, but exercises the full parameter parsing path.
func TestLogsAcceptsTailAndEnvParams(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := oldNewServer(t, k8sClient)
	h := srv.Handler()

	doRequest(h, http.MethodPost, "/api/apps", map[string]any{
		"name":      "params-app",
		"namespace": "default",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	// tail=50 env=production with no matching pods still returns 404 "no pods"
	// but the parsing shouldn't fail.
	w := doRequest(h, http.MethodGet, "/api/apps/params-app/logs?namespace=default&tail=50&env=production", nil)
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
// matching Pod object exists. The Pod doesn't need a running container —
// the fake clientset returns synthetic log content regardless.
func TestLogsSSEHeadersWithPod(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := oldNewServer(t, k8sClient)
	h := srv.Handler()

	// Create the App.
	doRequest(h, http.MethodPost, "/api/apps", map[string]any{
		"name":      "stream-app",
		"namespace": "default",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	// Create a Pod in envtest matching the label selector used by handleLogs.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stream-app-pod-1",
			Namespace: "default",
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

	// follow=false so the stream terminates quickly on the fake clientset's
	// single-response body.
	w := doRequest(h, http.MethodGet, "/api/apps/stream-app/logs?namespace=default&env=production&tail=10", nil)
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

	// Parse the first SSE event's JSON payload and verify structure.
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

	// Create App via the normal auth path.
	doRequest(h, http.MethodPost, "/api/apps", map[string]any{
		"name":      "tok-app",
		"namespace": "default",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	// No Authorization header, but ?token=... should authenticate and
	// then reach the handler which 404s because no pods exist.
	path := "/api/apps/tok-app/logs?namespace=default&token=" + token
	w := doRequestWithToken(h, http.MethodGet, path, nil, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 (auth passed, no pods), got %d: %s", w.Code, w.Body.String())
	}
}

// TestLogsRequiresAuth verifies that /logs still requires authentication —
// the query-param token fallback doesn't weaken the auth requirement.
func TestLogsRequiresAuth(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := oldNewServer(t, k8sClient)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodGet, "/api/apps/anything/logs", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", w.Code)
	}
}
