package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

// seedGitProvider creates a GitProvider CRD and its OAuth credential secrets
// so the OAuth handler can build an oauth2.Config.
func seedGitProvider(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()

	// Ensure mortise-system namespace.
	_ = c.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"},
	})

	// Create the OAuth credential secret.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gh-creds",
			Namespace: "mortise-system",
		},
		Data: map[string][]byte{
			"clientID":     []byte("test-client-id"),
			"clientSecret": []byte("test-client-secret"),
		},
	}
	if err := c.Create(ctx, secret); err != nil {
		t.Fatalf("create oauth secret: %v", err)
	}

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "github-main"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitHub,
			Host: "https://github.com",
			OAuth: mortisev1alpha1.OAuthConfig{
				ClientIDSecretRef:     mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "gh-creds", Key: "clientID"},
				ClientSecretSecretRef: mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "gh-creds", Key: "clientSecret"},
			},
			WebhookSecretRef: mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "gh-creds", Key: "clientID"},
		},
	}
	if err := c.Create(ctx, gp); err != nil {
		t.Fatalf("create GitProvider: %v", err)
	}
}

// --- Fix 1: OAuth CSRF ---

// TestOAuthAuthorizeSetsCookie verifies the authorize endpoint sets a state
// cookie and includes the same state in the redirect URL.
func TestOAuthAuthorizeSetsCookie(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	seedGitProvider(t, k8sClient)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/oauth/github-main/authorize", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", w.Code, w.Body.String())
	}

	var stateCookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "mortise_oauth_state" {
			stateCookie = c
			break
		}
	}
	if stateCookie == nil {
		t.Fatal("expected mortise_oauth_state cookie to be set")
	}
	if !stateCookie.HttpOnly {
		t.Error("state cookie must be HttpOnly")
	}
	if stateCookie.SameSite != http.SameSiteLaxMode {
		t.Error("state cookie must be SameSite=Lax")
	}
	if len(stateCookie.Value) != 64 { // 32 bytes hex-encoded
		t.Errorf("expected 64-char hex state, got %d chars", len(stateCookie.Value))
	}

	// The redirect Location should contain &state=<same value>.
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "state="+stateCookie.Value) {
		t.Errorf("redirect URL missing matching state param: %s", loc)
	}
}

// TestOAuthCallbackRejectsNoCookie verifies the callback rejects requests
// without the state cookie.
func TestOAuthCallbackRejectsNoCookie(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/oauth/github-main/callback?code=abc&state=xyz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without state cookie, got %d: %s", w.Code, w.Body.String())
	}
}

// TestOAuthCallbackRejectsMismatchedState verifies the callback rejects
// requests where the cookie state does not match the query state.
func TestOAuthCallbackRejectsMismatchedState(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/oauth/github-main/callback?code=abc&state=wrong", nil)
	req.AddCookie(&http.Cookie{Name: "mortise_oauth_state", Value: "correct"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for mismatched state, got %d: %s", w.Code, w.Body.String())
	}
}

// --- Fix 2: Body size limit ---

// TestBodySizeLimitRejects413 verifies that a >1MB POST to /api/apps returns
// 413 or 400 (MaxBytesReader surfaces as a decode error).
func TestBodySizeLimitRejects413(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	seedProject(t, k8sClient, "limit-test")
	h := srv.Handler()

	// Build a body that exceeds 1MB.
	bigBody := `{"name":"test","spec":{"source":{"type":"image","image":"` + strings.Repeat("x", 1<<20+1) + `"}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/limit-test/apps", strings.NewReader(bigBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	// http.MaxBytesReader causes either 413 (if the framework handles it) or
	// 400 (because json.Decode fails with a max-bytes error). Both are acceptable
	// rejections. 201 would be a failure.
	if w.Code == http.StatusCreated || w.Code == http.StatusOK {
		t.Fatalf("expected rejection for >1MB body, got %d", w.Code)
	}
	if w.Code != http.StatusRequestEntityTooLarge && w.Code != http.StatusBadRequest {
		t.Fatalf("expected 413 or 400, got %d: %s", w.Code, w.Body.String())
	}
}

// --- Fix 3: App name validation ---

// TestCreateAppInvalidName verifies the API rejects invalid app names with 400.
func TestCreateAppInvalidName(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	seedProject(t, k8sClient, "val-test")
	h := srv.Handler()

	cases := []struct {
		label string
		name  string
	}{
		{"empty", ""},
		{"uppercase", "BadApp"},
		{"dots", "my.app"},
		{"leading-hyphen", "-bad"},
		{"trailing-hyphen", "bad-"},
		{"too-long", strings.Repeat("a", 54)}, // exceeds maxAppNameLen=53
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			w := doRequest(h, http.MethodPost, "/api/projects/val-test/apps", map[string]any{
				"name": tc.name,
				"spec": map[string]any{
					"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
				},
			})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for name %q, got %d: %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

// TestCreateAppValidName confirms a valid name passes.
func TestCreateAppValidName(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	seedProject(t, k8sClient, "val-ok")
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/projects/val-ok/apps", map[string]any{
		"name": "good-app",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCreateAppInvalidEnvironmentName verifies env name validation on app creation.
func TestCreateAppInvalidEnvironmentName(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	seedProject(t, k8sClient, "env-val")
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/projects/env-val/apps", map[string]any{
		"name": "valid-app",
		"spec": map[string]any{
			"source":       map[string]any{"type": "image", "image": "nginx:1.25.0"},
			"environments": []map[string]any{{"name": "BAD_ENV"}},
		},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid env name, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp["error"], "environments[0].name") {
		t.Errorf("error should mention environments[0].name, got: %s", resp["error"])
	}
}
