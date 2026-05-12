package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/mortise-org/mortise/internal/api"
	"github.com/mortise-org/mortise/internal/auth"
	"github.com/mortise-org/mortise/internal/authz"
)

type staticAuthProvider struct {
	principal auth.Principal
}

func (s staticAuthProvider) Authenticate(ctx context.Context, creds auth.Credentials) (auth.Principal, error) {
	return auth.Principal{}, fmt.Errorf("not implemented")
}

func (s staticAuthProvider) Principal(ctx context.Context, session auth.SessionToken) (auth.Principal, error) {
	return s.principal, nil
}

func (s staticAuthProvider) ListUsers(ctx context.Context) ([]auth.User, error) {
	return nil, nil
}

func (s staticAuthProvider) InviteUser(ctx context.Context, email string, role auth.Role) (auth.InviteLink, error) {
	return auth.InviteLink{}, fmt.Errorf("not implemented")
}

func (s staticAuthProvider) RevokeUser(ctx context.Context, userID string) error {
	return fmt.Errorf("not implemented")
}

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
	user, ok := resp["user"].(map[string]any)
	if !ok {
		t.Fatalf("expected user object in login response, got %#v", resp["user"])
	}
	for _, key := range []string{"id", "email", "role", "passwordGen"} {
		if _, ok := user[key]; !ok {
			t.Fatalf("expected login response user.%s, got %#v", key, user)
		}
	}
	for _, key := range []string{"ID", "Email", "Role", "PasswordGen"} {
		if _, ok := user[key]; ok {
			t.Fatalf("unexpected legacy login response key %q in %#v", key, user)
		}
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

func TestRefreshAllowsRecentlyExpiredToken(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	if err := authProvider.CreateUser(ctx, "user@example.com", "pass1234", auth.RoleMember); err != nil {
		t.Fatalf("create user: %v", err)
	}
	principal, err := authProvider.Authenticate(ctx, auth.Credentials{Email: "user@example.com", Password: "pass1234"})
	if err != nil {
		t.Fatalf("authenticate user: %v", err)
	}

	// Generate one token first so the signing key secret exists, then mint an
	// already-expired token that is still within the refresh leeway window.
	if _, err := jwtHelper.GenerateToken(ctx, principal); err != nil {
		t.Fatalf("generate baseline token: %v", err)
	}
	var secret corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "mortise-jwt-key", Namespace: "mortise-system"}, &secret); err != nil {
		t.Fatalf("read jwt signing key: %v", err)
	}

	now := time.Now()
	claims := jwt.MapClaims{
		"sub":     principal.ID,
		"email":   principal.Email,
		"role":    string(principal.Role),
		"pwd_gen": principal.PasswordGen,
		"iss":     "mortise",
		"aud":     "mortise-api",
		"iat":     now.Add(-2 * time.Hour).Unix(),
		"exp":     now.Add(-1 * time.Hour).Unix(),
	}
	expiredToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret.Data["signing-key"])
	if err != nil {
		t.Fatalf("sign expired token: %v", err)
	}

	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodPost, "/api/auth/refresh", nil, expiredToken)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 refreshing recently expired token, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Token string         `json:"token"`
		User  auth.Principal `json:"user"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode refresh response: %v", err)
	}
	if resp.Token == "" {
		t.Fatal("expected refreshed token")
	}
	refreshed, _, err := jwtHelper.ValidateToken(ctx, resp.Token)
	if err != nil {
		t.Fatalf("validate refreshed token: %v", err)
	}
	if refreshed.Email != principal.Email {
		t.Fatalf("expected refreshed JWT for %q, got %q", principal.Email, refreshed.Email)
	}
	if resp.User.Email != principal.Email {
		t.Fatalf("expected refreshed principal %q, got %q", principal.Email, resp.User.Email)
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

func TestRefreshRoleChangeUsesCurrentRole(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	if err := authProvider.CreateUser(ctx, "admin@example.com", "adminpass1", auth.RoleAdmin); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := authProvider.CreateUser(ctx, "user@example.com", "pass1234", auth.RoleAdmin); err != nil {
		t.Fatalf("create user: %v", err)
	}
	adminPrincipal, err := authProvider.Authenticate(ctx, auth.Credentials{Email: "admin@example.com", Password: "adminpass1"})
	if err != nil {
		t.Fatalf("authenticate admin: %v", err)
	}
	adminToken, err := jwtHelper.GenerateToken(ctx, adminPrincipal)
	if err != nil {
		t.Fatalf("generate admin token: %v", err)
	}

	principal, err := authProvider.Authenticate(ctx, auth.Credentials{Email: "user@example.com", Password: "pass1234"})
	if err != nil {
		t.Fatalf("authenticate user: %v", err)
	}
	token, err := jwtHelper.GenerateToken(ctx, principal)
	if err != nil {
		t.Fatalf("generate user token: %v", err)
	}

	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodPatch, "/api/admin/users/user@example.com", map[string]any{"role": "member"}, adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 updating role, got %d: %s", w.Code, w.Body.String())
	}

	w = doRequestWithToken(h, http.MethodPost, "/api/auth/refresh", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on refresh, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Token string         `json:"token"`
		User  auth.Principal `json:"user"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode refresh response: %v", err)
	}
	if resp.User.Role != auth.RoleMember {
		t.Fatalf("expected refreshed role %q, got %q", auth.RoleMember, resp.User.Role)
	}

	if resp.Token == "" {
		t.Fatal("expected refreshed token")
	}
	refreshed, _, err := jwtHelper.ValidateToken(ctx, resp.Token)
	if err != nil {
		t.Fatalf("validate refreshed token: %v", err)
	}
	if refreshed.Role != auth.RoleMember {
		t.Fatalf("expected refreshed token role %q, got %q", auth.RoleMember, refreshed.Role)
	}
}

func TestRefreshRejectedAfterUserRevoked(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	if err := authProvider.CreateUser(ctx, "user@example.com", "pass1234", auth.RoleMember); err != nil {
		t.Fatalf("create user: %v", err)
	}
	principal, err := authProvider.Authenticate(ctx, auth.Credentials{Email: "user@example.com", Password: "pass1234"})
	if err != nil {
		t.Fatalf("authenticate user: %v", err)
	}
	token, err := jwtHelper.GenerateToken(ctx, principal)
	if err != nil {
		t.Fatalf("generate user token: %v", err)
	}

	if err := authProvider.RevokeUser(ctx, "user@example.com"); err != nil {
		t.Fatalf("revoke user: %v", err)
	}

	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodPost, "/api/auth/refresh", nil, token)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after revocation, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRefreshRejectsProviderWithoutCurrentPrincipal(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	jwtHelper := auth.NewJWTHelper(k8sClient)
	principal := auth.Principal{
		ID:          "user@example.com",
		Email:       "user@example.com",
		Role:        auth.RoleAdmin,
		PasswordGen: 0,
	}
	token, err := jwtHelper.GenerateToken(ctx, principal)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	srv := api.NewServer(
		k8sClient,
		fake.NewClientset(),
		nil,
		nil,
		staticAuthProvider{principal: principal},
		jwtHelper,
		nil,
		authz.NewNativePolicyEngine(k8sClient),
	)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodPost, "/api/auth/refresh", nil, token)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when provider cannot revalidate refresh, got %d: %s", w.Code, w.Body.String())
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
