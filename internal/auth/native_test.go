package auth

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func setup(t *testing.T) (*NativeAuthProvider, context.Context) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	// Pre-create the mortise-system namespace so secrets can live there.
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: namespace},
	}

	c := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ns).
		Build()
	return NewNativeAuthProvider(c), context.Background()
}

func TestCreateAndAuthenticate(t *testing.T) {
	provider, ctx := setup(t)

	if err := provider.CreateUser(ctx, "alice@example.com", "s3cret", RoleAdmin); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	p, err := provider.Authenticate(ctx, Credentials{Email: "alice@example.com", Password: "s3cret"})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if p.Email != "alice@example.com" {
		t.Errorf("expected email alice@example.com, got %s", p.Email)
	}
	if p.Role != RoleAdmin {
		t.Errorf("expected role admin, got %s", p.Role)
	}
}

func TestAuthenticateWrongPassword(t *testing.T) {
	provider, ctx := setup(t)

	if err := provider.CreateUser(ctx, "bob@example.com", "correct", RoleMember); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	_, err := provider.Authenticate(ctx, Credentials{Email: "bob@example.com", Password: "wrong"})
	if err == nil {
		t.Fatal("expected error for wrong password")
	}
}

func TestAuthenticateNoUser(t *testing.T) {
	provider, ctx := setup(t)

	_, err := provider.Authenticate(ctx, Credentials{Email: "nobody@example.com", Password: "test"})
	if err == nil {
		t.Fatal("expected error for missing user")
	}
}

func TestJWTRoundTrip(t *testing.T) {
	provider, ctx := setup(t)

	original := Principal{ID: "alice@example.com", Email: "alice@example.com", Role: RoleAdmin}
	token, err := provider.GenerateSessionToken(ctx, original)
	if err != nil {
		t.Fatalf("GenerateSessionToken: %v", err)
	}

	got, err := provider.Principal(ctx, token)
	if err != nil {
		t.Fatalf("Principal: %v", err)
	}
	if got.ID != original.ID || got.Email != original.Email || got.Role != original.Role {
		t.Errorf("principal mismatch: got %+v, want %+v", got, original)
	}
}

func TestListUsers(t *testing.T) {
	provider, ctx := setup(t)

	for _, email := range []string{"a@example.com", "b@example.com"} {
		if err := provider.CreateUser(ctx, email, "pass", RoleMember); err != nil {
			t.Fatalf("CreateUser(%s): %v", email, err)
		}
	}

	users, err := provider.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 2 {
		t.Errorf("expected 2 users, got %d", len(users))
	}
}

func TestInviteUser(t *testing.T) {
	provider, ctx := setup(t)

	link, err := provider.InviteUser(ctx, "new@example.com", RoleMember)
	if err != nil {
		t.Fatalf("InviteUser: %v", err)
	}
	if link.URL == "" {
		t.Error("expected non-empty invite URL")
	}
	if link.ExpiresAt == 0 {
		t.Error("expected non-zero expiry")
	}
}

func TestRevokeUser(t *testing.T) {
	provider, ctx := setup(t)

	if err := provider.CreateUser(ctx, "doomed@example.com", "pass", RoleMember); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if err := provider.RevokeUser(ctx, "doomed@example.com"); err != nil {
		t.Fatalf("RevokeUser: %v", err)
	}

	_, err := provider.Authenticate(ctx, Credentials{Email: "doomed@example.com", Password: "pass"})
	if err == nil {
		t.Fatal("expected error after revocation")
	}
}

func TestRevokeNonexistent(t *testing.T) {
	provider, ctx := setup(t)

	err := provider.RevokeUser(ctx, "ghost@example.com")
	if err == nil {
		t.Fatal("expected error revoking non-existent user")
	}
}

func TestBcryptVerification(t *testing.T) {
	provider, ctx := setup(t)

	if err := provider.CreateUser(ctx, "hash@example.com", "mypassword", RoleMember); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Correct password succeeds
	if _, err := provider.Authenticate(ctx, Credentials{Email: "hash@example.com", Password: "mypassword"}); err != nil {
		t.Fatalf("expected success: %v", err)
	}

	// Wrong password fails
	if _, err := provider.Authenticate(ctx, Credentials{Email: "hash@example.com", Password: "notmypassword"}); err == nil {
		t.Fatal("expected failure for wrong password")
	}
}
