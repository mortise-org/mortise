package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func TestIssueSSEToken(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, token := newTestServer(t, k8sClient)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodPost, "/api/auth/sse-token", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	sseToken, _ := resp["token"].(string)
	if sseToken == "" {
		t.Fatal("expected non-empty SSE token")
	}
	if !strings.HasPrefix(sseToken, "msse_") {
		t.Fatalf("expected msse_ prefix, got %q", sseToken)
	}
	expiresIn, _ := resp["expiresIn"].(float64)
	if expiresIn != 30 {
		t.Fatalf("expected expiresIn=30, got %v", expiresIn)
	}
}

func TestIssueSSETokenRequiresAuth(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodPost, "/api/auth/sse-token", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", w.Code)
	}
}

func TestSSETokenRedeemAndSingleUse(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, token := newTestServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequestWithToken(h, http.MethodPost, "/api/auth/sse-token", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("issue: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	sseToken := resp["token"].(string)

	firstPath := "/api/projects/default/apps/missing/logs?env=production&token=" + url.QueryEscape(sseToken)
	first := doRequestWithToken(h, http.MethodGet, firstPath, nil, "")
	if first.Code != http.StatusNotFound {
		t.Fatalf("expected first SSE-token request to reach handler and 404, got %d: %s", first.Code, first.Body.String())
	}

	second := doRequestWithToken(h, http.MethodGet, firstPath, nil, "")
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("expected redeemed SSE token to be rejected on reuse, got %d: %s", second.Code, second.Body.String())
	}
}

func TestIssueSSETokenRejectsRevokedUser(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	if err := authProvider.CreateUser(ctx, "revoked@example.com", "pass1234", auth.RoleAdmin); err != nil {
		t.Fatalf("create user: %v", err)
	}
	principal, _ := authProvider.Authenticate(ctx, auth.Credentials{Email: "revoked@example.com", Password: "pass1234"})
	token, _ := jwtHelper.GenerateToken(ctx, principal)
	if err := authProvider.RevokeUser(ctx, "revoked@example.com"); err != nil {
		t.Fatalf("revoke user: %v", err)
	}

	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodPost, "/api/auth/sse-token", nil, token)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for revoked user SSE token issue, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRefreshToken(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	if err := authProvider.CreateUser(ctx, "refresh@example.com", "pass1234", auth.RoleAdmin); err != nil {
		t.Fatalf("create user: %v", err)
	}
	principal, _ := authProvider.Authenticate(ctx, auth.Credentials{Email: "refresh@example.com", Password: "pass1234"})
	token, _ := jwtHelper.GenerateToken(ctx, principal)

	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodPost, "/api/auth/refresh", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	newToken, _ := resp["token"].(string)
	if newToken == "" {
		t.Fatal("expected non-empty refreshed token")
	}

	// New token should work for authenticated requests.
	w2 := doRequestWithToken(h, http.MethodGet, "/api/projects", nil, newToken)
	if w2.Code != http.StatusOK {
		t.Fatalf("refreshed token should work, got %d", w2.Code)
	}
}

func TestRefreshTokenRequiresAuth(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodPost, "/api/auth/refresh", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", w.Code)
	}
}

func TestRefreshTokenRejectsRevokedUser(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	if err := authProvider.CreateUser(ctx, "refresh@example.com", "pass1234", auth.RoleAdmin); err != nil {
		t.Fatalf("create user: %v", err)
	}
	principal, _ := authProvider.Authenticate(ctx, auth.Credentials{Email: "refresh@example.com", Password: "pass1234"})
	token, _ := jwtHelper.GenerateToken(ctx, principal)
	if err := authProvider.RevokeUser(ctx, "refresh@example.com"); err != nil {
		t.Fatalf("revoke user: %v", err)
	}

	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodPost, "/api/auth/refresh", nil, token)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for revoked user refresh, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRefreshTokenRejectsProviderWithoutRefreshPrincipal(t *testing.T) {
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
