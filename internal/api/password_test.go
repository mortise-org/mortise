package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/mortise-org/mortise/internal/auth"
)

func TestResetUserPassword_EmptyPassword(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	ctx := context.Background()
	native := auth.NewNativeAuthProvider(k8sClient)
	if err := native.CreateUser(ctx, "target@example.com", "original1", auth.RoleMember); err != nil {
		t.Fatalf("create user: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/admin/users/target@example.com/password", map[string]string{})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty password, got %d: %s", w.Code, w.Body.String())
	}
}

func TestResetUserPassword_Success(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	ctx := context.Background()
	native := auth.NewNativeAuthProvider(k8sClient)
	if err := native.CreateUser(ctx, "target@example.com", "original1", auth.RoleMember); err != nil {
		t.Fatalf("create user: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/admin/users/target@example.com/password", map[string]string{
		"password": "newpass123",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Errorf("expected status=ok, got %s", resp["status"])
	}

	if err := native.VerifyPassword(ctx, "target@example.com", "newpass123"); err != nil {
		t.Fatalf("new password should work: %v", err)
	}

	if err := native.VerifyPassword(ctx, "target@example.com", "original1"); err == nil {
		t.Fatal("old password should no longer work")
	}
}

func TestResetUserPassword_TooShort(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	ctx := context.Background()
	native := auth.NewNativeAuthProvider(k8sClient)
	if err := native.CreateUser(ctx, "target@example.com", "original1", auth.RoleMember); err != nil {
		t.Fatalf("create user: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/admin/users/target@example.com/password", map[string]string{
		"password": "short",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for short password, got %d: %s", w.Code, w.Body.String())
	}
}

func TestResetUserPassword_NotFound(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/admin/users/nobody@example.com/password", map[string]string{
		"password": "somepassword123",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-existent user, got %d: %s", w.Code, w.Body.String())
	}
}

func TestChangePassword_Success(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, token := newTestServer(t, k8sClient)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodPost, "/api/me/password", map[string]string{
		"current_password": "testpass",
		"new_password":     "changed123",
	}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	native := auth.NewNativeAuthProvider(k8sClient)
	if err := native.VerifyPassword(context.Background(), "test@example.com", "changed123"); err != nil {
		t.Fatalf("new password should work: %v", err)
	}
}

func TestChangePassword_WrongCurrent(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, token := newTestServer(t, k8sClient)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodPost, "/api/me/password", map[string]string{
		"current_password": "wrongpass",
		"new_password":     "changed123",
	}, token)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong current password, got %d: %s", w.Code, w.Body.String())
	}
}

func TestChangePassword_NewTooShort(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, token := newTestServer(t, k8sClient)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodPost, "/api/me/password", map[string]string{
		"current_password": "testpass",
		"new_password":     "short",
	}, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for short new password, got %d: %s", w.Code, w.Body.String())
	}
}

func TestChangePassword_MissingFields(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, token := newTestServer(t, k8sClient)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodPost, "/api/me/password", map[string]string{
		"new_password": "changed123",
	}, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing current_password, got %d: %s", w.Code, w.Body.String())
	}
}

func TestChangePassword_InvalidatesOldToken(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, token := newTestServer(t, k8sClient)
	h := srv.Handler()

	// Change password using the current token.
	w := doRequestWithToken(h, http.MethodPost, "/api/me/password", map[string]string{
		"current_password": "testpass",
		"new_password":     "changed123",
	}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// The old token should now be rejected.
	w = doRequestWithToken(h, http.MethodGet, "/api/projects", nil, token)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalidated token, got %d", w.Code)
	}
}

func TestResetPassword_InvalidatesOldToken(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, token := newTestServer(t, k8sClient)
	h := srv.Handler()

	// Admin resets their own password.
	w := doRequestWithToken(h, http.MethodPost, "/api/admin/users/test@example.com/password", map[string]string{
		"password": "adminreset1",
	}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// The old token should now be rejected.
	w = doRequestWithToken(h, http.MethodGet, "/api/projects", nil, token)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalidated token, got %d", w.Code)
	}
}
