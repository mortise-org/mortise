package api_test

import (
	"context"
	"net/http"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/mortise-org/mortise/internal/api"
	"github.com/mortise-org/mortise/internal/auth"
	"github.com/mortise-org/mortise/internal/authz"
)

func TestUpdateUserRoleRejectsDemotingLastAdmin(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPatch, "/api/admin/users/test@example.com", map[string]any{
		"role": "member",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeleteUserRejectsDeletingLastAdmin(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodDelete, "/api/admin/users/test@example.com", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUpdateUserRoleAllowsDemotingAdminWhenAnotherAdminExists(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	srv, token := newTestServer(t, k8sClient)
	h := srv.Handler()

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	if err := authProvider.CreateUser(ctx, "second-admin@example.com", "testpass2", auth.RoleAdmin); err != nil {
		t.Fatalf("create second admin: %v", err)
	}

	w := doRequestWithToken(h, http.MethodPatch, "/api/admin/users/test@example.com", map[string]any{
		"role": "member",
	}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeleteUserAllowsDeletingAdminWhenAnotherAdminExists(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	if err := authProvider.CreateUser(ctx, "admin1@example.com", "testpass1", auth.RoleAdmin); err != nil {
		t.Fatalf("create admin1: %v", err)
	}
	if err := authProvider.CreateUser(ctx, "admin2@example.com", "testpass2", auth.RoleAdmin); err != nil {
		t.Fatalf("create admin2: %v", err)
	}
	principal, err := authProvider.Authenticate(ctx, auth.Credentials{Email: "admin1@example.com", Password: "testpass1"})
	if err != nil {
		t.Fatalf("authenticate admin1: %v", err)
	}
	token, err := jwtHelper.GenerateToken(ctx, principal)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodDelete, "/api/admin/users/admin1@example.com", nil, token)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}
