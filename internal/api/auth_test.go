package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/MC-Meesh/mortise/internal/api"
	"github.com/MC-Meesh/mortise/internal/auth"
)

// TestSetupFirstUser exercises the /api/auth/setup endpoint for the first-user flow.
func TestSetupFirstUser(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	srv := api.NewServer(k8sClient, fake.NewClientset(), authProvider, jwtHelper, nil)
	h := srv.Handler()

	// Setup should succeed when no users exist.
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

	srv := api.NewServer(k8sClient, fake.NewClientset(), authProvider, jwtHelper, nil)
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

	srv := api.NewServer(k8sClient, fake.NewClientset(), authProvider, jwtHelper, nil)
	h := srv.Handler()

	body := map[string]any{"email": "user@example.com", "password": "wrongpass"}
	w := doRequestWithToken(h, http.MethodPost, "/api/auth/login", body, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// TestProtectedRouteRequiresToken verifies /api/apps requires auth.
func TestProtectedRouteRequiresToken(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	srv := api.NewServer(k8sClient, fake.NewClientset(), authProvider, jwtHelper, nil)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodGet, "/api/apps?namespace=default", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", w.Code)
	}

	w = doRequestWithToken(h, http.MethodGet, "/api/apps?namespace=default", nil, "garbage-token")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with invalid token, got %d", w.Code)
	}
}

// TestProtectedRouteAcceptsValidToken verifies a real JWT works.
func TestProtectedRouteAcceptsValidToken(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	if err := authProvider.CreateUser(ctx, "user@example.com", "pass123", auth.RoleAdmin); err != nil {
		t.Fatalf("create user: %v", err)
	}
	principal, _ := authProvider.Authenticate(ctx, auth.Credentials{Email: "user@example.com", Password: "pass123"})
	token, _ := jwtHelper.GenerateToken(ctx, principal)

	srv := api.NewServer(k8sClient, fake.NewClientset(), authProvider, jwtHelper, nil)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodGet, "/api/apps?namespace=default", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid token, got %d: %s", w.Code, w.Body.String())
	}
}
