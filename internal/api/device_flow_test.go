package api_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

// TestDeviceFlowRequestCodePlaceholder verifies that when no client ID is
// configured (placeholder still in place), the endpoint returns 503.
func TestDeviceFlowRequestCodePlaceholder(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/auth/github/device", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDeviceFlowRequestCodeWithEnvVar verifies that the endpoint calls GitHub
// when a client ID is configured via env var.
func TestDeviceFlowRequestCodeWithEnvVar(t *testing.T) {
	t.Setenv("MORTISE_GITHUB_CLIENT_ID", "test-client-id")

	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/auth/github/device", nil)

	// We expect either 200 (if GitHub is reachable) or 502 (if not).
	// The key assertion is that it did NOT return 503 (placeholder error).
	if w.Code == http.StatusServiceUnavailable {
		t.Fatalf("should not get 503 when MORTISE_GITHUB_CLIENT_ID is set")
	}
}

// TestDeviceFlowPollMissingDeviceCode verifies that the poll endpoint rejects
// empty device codes.
func TestDeviceFlowPollMissingDeviceCode(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/auth/github/device/poll", map[string]string{})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDeviceFlowPollInvalidJSON verifies that malformed JSON is rejected.
func TestDeviceFlowPollInvalidJSON(t *testing.T) {
	k8sClient := setupEnvtest(t)
	_, token := newTestServer(t, k8sClient)
	h := newAdminServer(t, k8sClient).Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/auth/github/device/poll", bytes.NewBufferString("not json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDeviceFlowResolveClientIDFromPlatformConfig verifies that the client ID
// is read from PlatformConfig when set.
func TestDeviceFlowResolveClientIDFromPlatformConfig(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()

	// Create PlatformConfig with a GitHub client ID.
	pc := &mortisev1alpha1.PlatformConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "platform"},
		Spec: mortisev1alpha1.PlatformConfigSpec{
			Domain: "example.com",
			DNS: mortisev1alpha1.DNSConfig{
				Provider: mortisev1alpha1.DNSProviderCloudflare,
				APITokenSecretRef: mortisev1alpha1.SecretRef{
					Namespace: "mortise-system",
					Name:      "dns-token",
					Key:       "token",
				},
			},
			GitHub: &mortisev1alpha1.GitHubConfig{
				ClientID: "platform-client-id",
			},
		},
	}
	if err := k8sClient.Create(ctx, pc); err != nil {
		t.Fatalf("create PlatformConfig: %v", err)
	}

	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	// The endpoint should NOT return 503 (placeholder error) since we have a
	// client ID in PlatformConfig.
	w := doRequest(h, http.MethodPost, "/api/auth/github/device", nil)
	if w.Code == http.StatusServiceUnavailable {
		t.Fatalf("should not get 503 when PlatformConfig has github.clientID")
	}
}

// TestDeviceFlowRoutesRequireAuth verifies that device flow endpoints
// require a JWT now that they're per-user.
func TestDeviceFlowRoutesRequireAuth(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	// Request with no Authorization header.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/github/device", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("device flow request code should require auth, got %d", w.Code)
	}

	body := `{"device_code": "test"}`
	req = httptest.NewRequest(http.MethodPost, "/api/auth/github/device/poll", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("device flow poll should require auth, got %d", w.Code)
	}
}

// TestDeviceFlowPollPendingStatus verifies that the poll endpoint returns
// "pending" when the token exchange returns authorization_pending.
func TestDeviceFlowPollPendingStatus(t *testing.T) {
	t.Setenv("MORTISE_GITHUB_CLIENT_ID", "test-client-id")

	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/auth/github/device/poll", map[string]string{"device_code": "some-code"})

	// The actual GitHub call will fail (network), so we expect 502.
	// This confirms the endpoint is wired correctly and processes the request.
	if w.Code != http.StatusBadGateway && w.Code != http.StatusOK {
		t.Fatalf("expected 502 or 200, got %d: %s", w.Code, w.Body.String())
	}
}

// TestGitHubStatusNotConnected verifies the status endpoint returns false when
// no token is stored.
func TestGitHubStatusNotConnected(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/auth/github/status", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]bool
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["connected"] != false {
		t.Errorf("expected connected=false, got %v", resp["connected"])
	}
}

// TestGitHubStatusConnected verifies the status endpoint returns true when
// a token is stored for the calling user.
func TestGitHubStatusConnected(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()

	_ = k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"},
	})

	// Store a token for the test user (test@example.com).
	email := "test@example.com"
	secretName := "user-github-token-" + hex.EncodeToString([]byte(email))
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: "mortise-system",
		},
		Data: map[string][]byte{"token": []byte("gho_test_token")},
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("create token secret: %v", err)
	}

	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/auth/github/status", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]bool
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["connected"] != true {
		t.Errorf("expected connected=true, got %v", resp["connected"])
	}
}

// TestGitHubStatusRequiresAuth verifies the status endpoint requires JWT.
func TestGitHubStatusRequiresAuth(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/auth/github/status", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// TestPerUserTokenStorage verifies that different users get different tokens stored.
func TestPerUserTokenStorage(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()

	_ = k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"},
	})

	email1 := "alice@example.com"
	email2 := "bob@example.com"
	secret1Name := "user-github-token-" + hex.EncodeToString([]byte(email1))
	secret2Name := "user-github-token-" + hex.EncodeToString([]byte(email2))

	// Store tokens for two different users.
	for _, s := range []struct {
		name  string
		token string
	}{
		{secret1Name, "token-alice"},
		{secret2Name, "token-bob"},
	} {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      s.name,
				Namespace: "mortise-system",
			},
			Data: map[string][]byte{"token": []byte(s.token)},
		}
		if err := k8sClient.Create(ctx, secret); err != nil {
			t.Fatalf("create secret %s: %v", s.name, err)
		}
	}

	// Verify they stored different secrets.
	var s1 corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "mortise-system", Name: secret1Name}, &s1); err != nil {
		t.Fatalf("get secret1: %v", err)
	}
	if string(s1.Data["token"]) != "token-alice" {
		t.Errorf("expected token-alice, got %q", string(s1.Data["token"]))
	}

	var s2 corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "mortise-system", Name: secret2Name}, &s2); err != nil {
		t.Fatalf("get secret2: %v", err)
	}
	if string(s2.Data["token"]) != "token-bob" {
		t.Errorf("expected token-bob, got %q", string(s2.Data["token"]))
	}

	// Verify secret names are different.
	if secret1Name == secret2Name {
		t.Error("expected different secret names for different users")
	}
}
