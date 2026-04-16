package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/MC-Meesh/mortise/internal/api"
)

func TestLogsNonexistentApp(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := api.NewServer(k8sClient, fake.NewClientset())
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
	srv := api.NewServer(k8sClient, fake.NewClientset())
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

func TestLogsSSEHeaders(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := api.NewServer(k8sClient, fake.NewClientset())
	h := srv.Handler()

	// Create an app with a pod — but envtest doesn't run real pods, so we'll get
	// the 404 "no pods" response. We test headers on the success path via
	// integration tests; here we just verify the app-exists-but-no-pods path
	// returns JSON, not SSE headers (since it's an error before streaming starts).
	doRequest(h, http.MethodPost, "/api/apps", map[string]any{
		"name":      "headers-app",
		"namespace": "default",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	w := doRequest(h, http.MethodGet, "/api/apps/headers-app/logs?namespace=default", nil)
	// Error responses should be JSON, not SSE.
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json for error response, got %s", ct)
	}
}
