package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/mortise-org/mortise/internal/api"
	"github.com/mortise-org/mortise/internal/auth"
	"github.com/mortise-org/mortise/internal/authz"
)

// TestAuthStatusSetupRequired verifies /api/auth/status flips from setupRequired=true
// to false once the first admin is created.
func TestAuthStatusSetupRequired(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodGet, "/api/auth/status", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on status, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["setupRequired"] != true {
		t.Fatalf("expected setupRequired=true before any user exists, got %v", resp["setupRequired"])
	}

	if err := authProvider.CreateUser(ctx, "admin@example.com", "initialpass", auth.RoleAdmin); err != nil {
		t.Fatalf("create user: %v", err)
	}
	w = doRequestWithToken(h, http.MethodGet, "/api/auth/status", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on status, got %d", w.Code)
	}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["setupRequired"] != false {
		t.Fatalf("expected setupRequired=false after user created, got %v", resp["setupRequired"])
	}
}

// TestSetupCreatesAdmin exercises the /api/auth/setup endpoint,
// verifying an admin user is created and a token is returned.
func TestSetupCreatesAdmin(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	body := map[string]any{"email": "admin@example.com", "password": "initialpass"}
	w := doRequestWithToken(h, http.MethodPost, "/api/auth/setup", body, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 on first setup, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["token"] == nil || resp["token"] == "" {
		t.Error("expected a token in the setup response")
	}

	// Second setup attempt should return 409.
	w = doRequestWithToken(h, http.MethodPost, "/api/auth/setup", body, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 on second setup, got %d", w.Code)
	}
}

// TestLoginValid verifies the login endpoint returns a JWT on valid credentials.
func TestLoginValid(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	if err := authProvider.CreateUser(ctx, "user@example.com", "secret123", auth.RoleMember); err != nil {
		t.Fatalf("create user: %v", err)
	}

	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	body := map[string]any{"email": "user@example.com", "password": "secret123"}
	w := doRequestWithToken(h, http.MethodPost, "/api/auth/login", body, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	token, _ := resp["token"].(string)
	if token == "" {
		t.Error("expected a non-empty token")
	}
}

// TestLoginInvalidCredentials verifies wrong password returns 401.
func TestLoginInvalidCredentials(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	if err := authProvider.CreateUser(ctx, "user@example.com", "correctpass", auth.RoleMember); err != nil {
		t.Fatalf("create user: %v", err)
	}

	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	body := map[string]any{"email": "user@example.com", "password": "wrongpass"}
	w := doRequestWithToken(h, http.MethodPost, "/api/auth/login", body, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// TestSetupRejectsShortPassword verifies setup returns 400 for passwords under 8 chars.
func TestSetupRejectsShortPassword(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	body := map[string]any{"email": "admin@example.com", "password": "short"}
	w := doRequestWithToken(h, http.MethodPost, "/api/auth/setup", body, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for short password, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCreateUserRejectsShortPassword verifies CreateUser rejects passwords under 8 chars.
func TestCreateUserRejectsShortPassword(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	err := authProvider.CreateUser(ctx, "user@example.com", "short", auth.RoleMember)
	if err == nil {
		t.Fatal("expected error for short password")
	}
}

// TestProtectedRouteRequiresToken verifies /api/projects requires auth.
func TestProtectedRouteRequiresToken(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodGet, "/api/projects", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", w.Code)
	}

	w = doRequestWithToken(h, http.MethodGet, "/api/projects", nil, "garbage-token")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with invalid token, got %d", w.Code)
	}
}

// TestRefreshValidToken verifies that a valid JWT can be refreshed.
func TestRefreshValidToken(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	if err := authProvider.CreateUser(ctx, "user@example.com", "pass1234", auth.RoleMember); err != nil {
		t.Fatalf("create user: %v", err)
	}
	principal, _ := authProvider.Authenticate(ctx, auth.Credentials{Email: "user@example.com", Password: "pass1234"})
	token, _ := jwtHelper.GenerateToken(ctx, principal)

	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodPost, "/api/auth/refresh", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on refresh, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	newToken, _ := resp["token"].(string)
	if newToken == "" {
		t.Error("expected a non-empty token in refresh response")
	}

	// The refreshed token should work for protected routes.
	w = doRequestWithToken(h, http.MethodGet, "/api/projects", nil, newToken)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with refreshed token, got %d", w.Code)
	}
}

// TestRefreshWithoutToken verifies that refresh without a token returns 401.
func TestRefreshWithoutToken(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodPost, "/api/auth/refresh", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", w.Code)
	}
}

// TestRefreshInvalidToken verifies that garbage tokens are rejected.
func TestRefreshInvalidToken(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodPost, "/api/auth/refresh", nil, "not-a-real-token")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with invalid token, got %d", w.Code)
	}
}

// TestRefreshAfterPasswordChange verifies that refresh fails when the password
// was changed after the token was issued.
func TestRefreshAfterPasswordChange(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	if err := authProvider.CreateUser(ctx, "user@example.com", "pass1234", auth.RoleMember); err != nil {
		t.Fatalf("create user: %v", err)
	}
	principal, _ := authProvider.Authenticate(ctx, auth.Credentials{Email: "user@example.com", Password: "pass1234"})
	token, _ := jwtHelper.GenerateToken(ctx, principal)

	if err := authProvider.UpdatePassword(ctx, "user@example.com", "newpass12"); err != nil {
		t.Fatalf("update password: %v", err)
	}

	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodPost, "/api/auth/refresh", nil, token)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after password change, got %d: %s", w.Code, w.Body.String())
	}
}

// TestProtectedRouteAcceptsValidToken verifies a real JWT works.
func TestProtectedRouteAcceptsValidToken(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	if err := authProvider.CreateUser(ctx, "user@example.com", "pass1234", auth.RoleAdmin); err != nil {
		t.Fatalf("create user: %v", err)
	}
	principal, _ := authProvider.Authenticate(ctx, auth.Credentials{Email: "user@example.com", Password: "pass1234"})
	token, _ := jwtHelper.GenerateToken(ctx, principal)

	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodGet, "/api/projects", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid token, got %d: %s", w.Code, w.Body.String())
	}
}
