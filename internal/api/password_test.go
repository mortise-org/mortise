package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/mortise-org/mortise/internal/auth"
)

func TestResetUserPassword_AutoGenerate(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	// Create a target user to reset.
	ctx := context.Background()
	native := auth.NewNativeAuthProvider(k8sClient)
	if err := native.CreateUser(ctx, "target@example.com", "original1", auth.RoleMember); err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Reset without specifying a password — should auto-generate.
	w := doRequest(h, http.MethodPost, "/api/admin/users/target@example.com/password", map[string]string{})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	newPass := resp["password"]
	if len(newPass) < 16 {
		t.Fatalf("expected auto-generated password of at least 16 chars, got %q", newPass)
	}

	// Verify the new password works.
	if err := native.VerifyPassword(ctx, "target@example.com", newPass); err != nil {
		t.Fatalf("new password should work: %v", err)
	}

	// Verify old password no longer works.
	if err := native.VerifyPassword(ctx, "target@example.com", "original1"); err == nil {
		t.Fatal("old password should no longer work")
	}
}

func TestResetUserPassword_Specified(t *testing.T) {
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
	if resp["password"] != "newpass123" {
		t.Errorf("expected password=newpass123, got %s", resp["password"])
	}

	if err := native.VerifyPassword(ctx, "target@example.com", "newpass123"); err != nil {
		t.Fatalf("specified password should work: %v", err)
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

	w := doRequest(h, http.MethodPost, "/api/admin/users/nobody@example.com/password", map[string]string{})
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
