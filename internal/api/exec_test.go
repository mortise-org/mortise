package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/MC-Meesh/mortise/internal/api"
	"github.com/MC-Meesh/mortise/internal/auth"
	"github.com/MC-Meesh/mortise/internal/authz"
)

// TestServerCarriesInjectedRESTConfig verifies the constructor plumbs the
// rest.Config through onto the Server (instead of the handler calling
// rest.InClusterConfig() per request at runtime).
func TestServerCarriesInjectedRESTConfig(t *testing.T) {
	k8sClient := setupEnvtest(t)
	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	cfg := &rest.Config{Host: "https://example.test"}

	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, cfg, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine())
	if srv.RESTConfig() == nil {
		t.Fatal("expected Server.RESTConfig() to return the injected config, got nil")
	}
	if srv.RESTConfig().Host != "https://example.test" {
		t.Errorf("expected host https://example.test, got %q", srv.RESTConfig().Host)
	}
}

// TestExecEmptyCommand verifies the handler rejects an empty command list.
func TestExecEmptyCommand(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/anything/exec", map[string]any{
		"command": []string{},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty command, got %d: %s", w.Code, w.Body.String())
	}
}

// TestExecInvalidJSON verifies the handler rejects malformed JSON.
func TestExecInvalidJSON(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequestRawBody(h, http.MethodPost, "/api/projects/default/apps/anything/exec", "{bad json", testToken)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d: %s", w.Code, w.Body.String())
	}
}

// TestExecMissingProject verifies exec returns 404 for a nonexistent project.
func TestExecMissingProject(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/projects/ghost/apps/anything/exec", map[string]any{
		"command": []string{"ls"},
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing project, got %d: %s", w.Code, w.Body.String())
	}
}

// TestExecRejectsWhenNoRESTConfig verifies the handler fails fast with 500
// (not a panic, not a silent in-cluster fallback) when the server was built
// without a rest.Config — e.g. in test harnesses that don't exercise exec.
func TestExecRejectsWhenNoRESTConfig(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	body := map[string]any{"command": []string{"ls"}}
	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/anything/exec", body)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when server has no rest.Config, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] == "" {
		t.Errorf("expected error message in body")
	}
}
