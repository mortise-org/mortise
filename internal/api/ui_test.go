package api_test

import (
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/mortise-org/mortise/internal/api"
	"github.com/mortise-org/mortise/internal/auth"
	"github.com/mortise-org/mortise/internal/authz"
)

// TestUIRootServesIndex verifies the root path returns the SvelteKit index.html
// when a UI filesystem is provided, with no auth required.
func TestUIRootServesIndex(t *testing.T) {
	k8sClient := setupEnvtest(t)
	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)

	uiFS := fstest.MapFS{
		"index.html":         &fstest.MapFile{Data: []byte(`<!DOCTYPE html><html><body>mortise ui</body></html>`)},
		"_app/immutable.css": &fstest.MapFile{Data: []byte("body {}")},
	}

	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, uiFS, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodGet, "/", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for UI root, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "mortise ui") {
		t.Errorf("expected index.html content, got %q", w.Body.String())
	}
}

// TestUISPAFallback verifies unknown paths fall back to index.html (SPA routing).
func TestUISPAFallback(t *testing.T) {
	k8sClient := setupEnvtest(t)
	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)

	uiFS := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(`<!DOCTYPE html><html><body>mortise spa</body></html>`)},
	}

	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, uiFS, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	// Request a SPA route that doesn't exist as a file.
	w := doRequestWithToken(h, http.MethodGet, "/apps/my-app", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for SPA fallback, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "mortise spa") {
		t.Errorf("expected index.html content on fallback, got %q", w.Body.String())
	}
}

// TestUIDoesNotInterceptAPI verifies /api routes still work when UI is enabled.
func TestUIDoesNotInterceptAPI(t *testing.T) {
	k8sClient := setupEnvtest(t)
	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)

	uiFS := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(`<html></html>`)},
	}

	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, uiFS, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	// /api/projects should still require auth (not be caught by the UI handler).
	w := doRequestWithToken(h, http.MethodGet, "/api/projects", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for /api without token, got %d", w.Code)
	}
}

// TestNoUIFS verifies the server works without a UI filesystem (API-only mode).
func TestNoUIFS(t *testing.T) {
	k8sClient := setupEnvtest(t)
	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)

	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	// Root should 404 since no UI is mounted.
	w := doRequestWithToken(h, http.MethodGet, "/", nil, "")
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 without UI, got %d", w.Code)
	}

	// API still works.
	w = doRequestWithToken(h, http.MethodPost, "/api/auth/login", map[string]any{"email": "a", "password": "b"}, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for bad login, got %d", w.Code)
	}
}
