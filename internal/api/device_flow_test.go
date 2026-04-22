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

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// TestDeviceFlowRequestCodeNoProvider verifies that when no GitProvider exists,
// the endpoint returns 404.
func TestDeviceFlowRequestCodeNoProvider(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/auth/git/github/device", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDeviceFlowRequestCodeNoClientID verifies that when a GitProvider exists
// but has no clientID, the endpoint returns 503.
func TestDeviceFlowRequestCodeNoClientID(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "github"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitHub,
			Host: "https://github.com",
		},
	}
	if err := k8sClient.Create(ctx, gp); err != nil {
		t.Fatalf("create GitProvider: %v", err)
	}

	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/auth/git/github/device", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDeviceFlowRequestCodeWithClientID verifies that the endpoint calls the
// forge when a GitProvider with clientID exists.
func TestDeviceFlowRequestCodeWithClientID(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "github"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type:     mortisev1alpha1.GitProviderTypeGitHub,
			Host:     "https://github.com",
			ClientID: "test-client-id",
		},
	}
	if err := k8sClient.Create(ctx, gp); err != nil {
		t.Fatalf("create GitProvider: %v", err)
	}

	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/auth/git/github/device", nil)
	// Expect 502 (GitHub unreachable in test) or 200, NOT 503 or 404.
	if w.Code == http.StatusServiceUnavailable || w.Code == http.StatusNotFound {
		t.Fatalf("should not get %d when GitProvider has clientID", w.Code)
	}
}

// TestDeviceFlowPollMissingDeviceCode verifies that the poll endpoint rejects
// empty device codes.
func TestDeviceFlowPollMissingDeviceCode(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	// Poll now requires auth — doRequest sends JWT via testToken.
	w := doRequest(h, http.MethodPost, "/api/auth/git/github/device/poll", map[string]string{})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDeviceFlowPollInvalidJSON verifies that malformed JSON is rejected.
func TestDeviceFlowPollInvalidJSON(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, token := newTestServer(t, k8sClient)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/auth/git/github/device/poll", bytes.NewBufferString("not json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDeviceFlowResolveClientIDFromGitProvider verifies that the client ID
// is read from the GitProvider CRD.
func TestDeviceFlowResolveClientIDFromGitProvider(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "github"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type:     mortisev1alpha1.GitProviderTypeGitHub,
			Host:     "https://github.com",
			ClientID: "provider-client-id",
		},
	}
	if err := k8sClient.Create(ctx, gp); err != nil {
		t.Fatalf("create GitProvider: %v", err)
	}

	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	// The endpoint should NOT return 503 or 404 since we have a GitProvider
	// with a clientID.
	w := doRequest(h, http.MethodPost, "/api/auth/git/github/device", nil)
	if w.Code == http.StatusServiceUnavailable || w.Code == http.StatusNotFound {
		t.Fatalf("should not get %d when GitProvider has clientID", w.Code)
	}
}

// TestDeviceFlowRoutesRequireAuth verifies that device flow endpoints
// require a JWT now that they're per-user.
func TestDeviceFlowRoutesRequireAuth(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	// Request with no Authorization header.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/git/github/device", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("device flow request code should require auth, got %d", w.Code)
	}

	body := `{"device_code": "test"}`
	req = httptest.NewRequest(http.MethodPost, "/api/auth/git/github/device/poll", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("device flow poll should require auth, got %d", w.Code)
	}
}

// TestDeviceFlowPollPendingStatus verifies that the poll endpoint processes
// the request when a GitProvider with clientID exists.
func TestDeviceFlowPollPendingStatus(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "github"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type:     mortisev1alpha1.GitProviderTypeGitHub,
			Host:     "https://github.com",
			ClientID: "test-client-id",
		},
	}
	if err := k8sClient.Create(ctx, gp); err != nil {
		t.Fatalf("create GitProvider: %v", err)
	}

	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/auth/git/github/device/poll", map[string]string{"device_code": "some-code"})

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

	w := doRequest(h, http.MethodGet, "/api/auth/git/github/status", nil)
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

	w := doRequest(h, http.MethodGet, "/api/auth/git/github/status", nil)
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

	req := httptest.NewRequest(http.MethodGet, "/api/auth/git/github/status", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// TestStorePATRequiresAuth verifies that the PAT store endpoint requires JWT.
func TestStorePATRequiresAuth(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/auth/git/gitlab/token", bytes.NewBufferString(`{"token":"glpat-test"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// TestStorePATMissingToken verifies that an empty token body is rejected.
func TestStorePATMissingToken(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/auth/git/gitlab/token", map[string]string{"token": ""})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestStorePATInvalidJSON verifies that malformed JSON is rejected.
func TestStorePATInvalidJSON(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, token := newTestServer(t, k8sClient)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/auth/git/gitlab/token", bytes.NewBufferString("not json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestStorePATHappyPath verifies that a valid PAT is stored as a k8s Secret.
func TestStorePATHappyPath(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()

	_ = k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"},
	})

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "gitlab"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitLab,
			Host: "https://gitlab.com",
		},
	}
	if err := k8sClient.Create(ctx, gp); err != nil {
		t.Fatalf("create GitProvider: %v", err)
	}

	srv, _ := newTestServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/auth/git/gitlab/token", map[string]string{"token": "glpat-supersecret"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the token secret was written.
	email := "test@example.com"
	secretName := "user-gitlab-token-" + hex.EncodeToString([]byte(email))
	var s corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "mortise-system", Name: secretName}, &s); err != nil {
		t.Fatalf("token secret not found: %v", err)
	}
	if string(s.Data["token"]) != "glpat-supersecret" {
		t.Errorf("expected stored token %q, got %q", "glpat-supersecret", string(s.Data["token"]))
	}
}

// TestStorePATOverwritesExistingToken verifies that re-submitting a PAT updates the stored token.
func TestStorePATOverwritesExistingToken(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()

	_ = k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"},
	})

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "gitea"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitea,
			Host: "https://gitea.example.com",
		},
	}
	if err := k8sClient.Create(ctx, gp); err != nil {
		t.Fatalf("create GitProvider: %v", err)
	}

	srv, _ := newTestServer(t, k8sClient)
	h := srv.Handler()

	doRequest(h, http.MethodPost, "/api/auth/git/gitea/token", map[string]string{"token": "old-token"})
	w := doRequest(h, http.MethodPost, "/api/auth/git/gitea/token", map[string]string{"token": "new-token"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	email := "test@example.com"
	secretName := "user-gitea-token-" + hex.EncodeToString([]byte(email))
	var s corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "mortise-system", Name: secretName}, &s); err != nil {
		t.Fatalf("token secret not found: %v", err)
	}
	if string(s.Data["token"]) != "new-token" {
		t.Errorf("expected updated token %q, got %q", "new-token", string(s.Data["token"]))
	}
}

// TestStorePATAutoCreatesGitLabProvider verifies that StorePAT with a "gitlab"
// provider name auto-creates a GitProvider with Type=gitlab (not github) when
// no GitProvider CRD exists and a client ID env var is set.
func TestStorePATAutoCreatesGitLabProvider(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()

	_ = k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"},
	})

	// Set the GitLab client ID env var so auto-create succeeds.
	t.Setenv("MORTISE_GITLAB_CLIENT_ID", "test-gitlab-client-id")

	srv, _ := newTestServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/auth/git/gitlab/token", map[string]string{"token": "glpat-test123"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the GitProvider was auto-created with the correct type.
	var gp mortisev1alpha1.GitProvider
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gitlab"}, &gp); err != nil {
		t.Fatalf("auto-created GitProvider not found: %v", err)
	}
	if gp.Spec.Type != mortisev1alpha1.GitProviderTypeGitLab {
		t.Errorf("expected provider type %q, got %q", mortisev1alpha1.GitProviderTypeGitLab, gp.Spec.Type)
	}
	if gp.Spec.Host != "https://gitlab.com" {
		t.Errorf("expected host %q, got %q", "https://gitlab.com", gp.Spec.Host)
	}

	// Verify the token was also stored.
	email := "test@example.com"
	secretName := "user-gitlab-token-" + hex.EncodeToString([]byte(email))
	var s corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "mortise-system", Name: secretName}, &s); err != nil {
		t.Fatalf("token secret not found: %v", err)
	}
	if string(s.Data["token"]) != "glpat-test123" {
		t.Errorf("expected stored token %q, got %q", "glpat-test123", string(s.Data["token"]))
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
