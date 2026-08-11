package auth

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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

	if err := provider.CreateUser(ctx, "alice@example.com", "s3cret12", RoleAdmin); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	p, err := provider.Authenticate(ctx, Credentials{Email: "alice@example.com", Password: "s3cret12"})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if p.Email != "alice@example.com" {
		t.Errorf("expected email alice@example.com, got %s", p.Email)
	}
	if p.Role != RoleAdmin {
		t.Errorf("expected role admin, got %s", p.Role)
	}
	if p.PasswordGen != 0 {
		t.Errorf("expected password_gen 0 for new user, got %d", p.PasswordGen)
	}
}

func TestAuthenticateWrongPassword(t *testing.T) {
	provider, ctx := setup(t)

	if err := provider.CreateUser(ctx, "bob@example.com", "correct1", RoleMember); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	_, err := provider.Authenticate(ctx, Credentials{Email: "bob@example.com", Password: "wrongpwd"})
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

	if err := provider.CreateUser(ctx, "alice@example.com", "s3cret12", RoleAdmin); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	original, err := provider.Authenticate(ctx, Credentials{Email: "alice@example.com", Password: "s3cret12"})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
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

func TestPrincipalReflectsCurrentRole(t *testing.T) {
	provider, ctx := setup(t)

	if err := provider.CreateUser(ctx, "alice@example.com", "s3cret12", RoleMember); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	principal, err := provider.Authenticate(ctx, Credentials{Email: "alice@example.com", Password: "s3cret12"})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	token, err := provider.GenerateSessionToken(ctx, principal)
	if err != nil {
		t.Fatalf("GenerateSessionToken: %v", err)
	}

	var secret corev1.Secret
	if err := provider.client.Get(ctx, types.NamespacedName{
		Name:      userSecretName("alice@example.com"),
		Namespace: namespace,
	}, &secret); err != nil {
		t.Fatalf("Get user secret: %v", err)
	}
	secret.Data["role"] = []byte(RoleAdmin)
	if err := provider.client.Update(ctx, &secret); err != nil {
		t.Fatalf("Update user secret: %v", err)
	}

	got, err := provider.Principal(ctx, token)
	if err != nil {
		t.Fatalf("Principal: %v", err)
	}
	if got.Role != RoleAdmin {
		t.Fatalf("expected refreshed principal role %q, got %q", RoleAdmin, got.Role)
	}
}

func TestListUsers(t *testing.T) {
	provider, ctx := setup(t)

	for _, email := range []string{"a@example.com", "b@example.com"} {
		if err := provider.CreateUser(ctx, email, "pass1234", RoleMember); err != nil {
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

	if err := provider.CreateUser(ctx, "doomed@example.com", "pass1234", RoleMember); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if err := provider.RevokeUser(ctx, "doomed@example.com"); err != nil {
		t.Fatalf("RevokeUser: %v", err)
	}

	_, err := provider.Authenticate(ctx, Credentials{Email: "doomed@example.com", Password: "pass1234"})
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

func TestUpdatePassword(t *testing.T) {
	provider, ctx := setup(t)

	if err := provider.CreateUser(ctx, "alice@example.com", "oldpass123", RoleMember); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if err := provider.UpdatePassword(ctx, "alice@example.com", "newpass456"); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}

	// Old password no longer works
	if _, err := provider.Authenticate(ctx, Credentials{Email: "alice@example.com", Password: "oldpass123"}); err == nil {
		t.Fatal("old password should no longer work")
	}

	// New password works
	if _, err := provider.Authenticate(ctx, Credentials{Email: "alice@example.com", Password: "newpass456"}); err != nil {
		t.Fatalf("new password should work: %v", err)
	}
}

func TestUpdatePasswordTooShort(t *testing.T) {
	provider, ctx := setup(t)

	if err := provider.CreateUser(ctx, "alice@example.com", "longpass", RoleMember); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	err := provider.UpdatePassword(ctx, "alice@example.com", "short")
	if err == nil {
		t.Fatal("expected error for short password")
	}
}

func TestUpdatePasswordNonexistent(t *testing.T) {
	provider, ctx := setup(t)

	err := provider.UpdatePassword(ctx, "ghost@example.com", "newpass123")
	if err == nil {
		t.Fatal("expected error for non-existent user")
	}
}

func TestVerifyPassword(t *testing.T) {
	provider, ctx := setup(t)

	if err := provider.CreateUser(ctx, "bob@example.com", "correct1", RoleMember); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if err := provider.VerifyPassword(ctx, "bob@example.com", "correct1"); err != nil {
		t.Fatalf("VerifyPassword should succeed: %v", err)
	}

	if err := provider.VerifyPassword(ctx, "bob@example.com", "wrong"); err == nil {
		t.Fatal("VerifyPassword should fail for wrong password")
	}

	if err := provider.VerifyPassword(ctx, "ghost@example.com", "anything"); err == nil {
		t.Fatal("VerifyPassword should fail for non-existent user")
	}
}

func TestPasswordChangeInvalidatesToken(t *testing.T) {
	provider, ctx := setup(t)

	if err := provider.CreateUser(ctx, "carol@example.com", "original1", RoleMember); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Generate a session token.
	principal, err := provider.Authenticate(ctx, Credentials{Email: "carol@example.com", Password: "original1"})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	token, err := provider.GenerateSessionToken(ctx, principal)
	if err != nil {
		t.Fatalf("GenerateSessionToken: %v", err)
	}

	// Token should work before password change.
	if _, err := provider.Principal(ctx, token); err != nil {
		t.Fatalf("token should be valid before password change: %v", err)
	}

	// Change password.
	if err := provider.UpdatePassword(ctx, "carol@example.com", "newpassword1"); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}

	// Token issued before password change should be rejected.
	if _, err := provider.Principal(ctx, token); err == nil {
		t.Fatal("token issued before password change should be rejected")
	}

	// A new token should work.
	principal, err = provider.Authenticate(ctx, Credentials{Email: "carol@example.com", Password: "newpassword1"})
	if err != nil {
		t.Fatalf("Authenticate with new password: %v", err)
	}
	newToken, err := provider.GenerateSessionToken(ctx, principal)
	if err != nil {
		t.Fatalf("GenerateSessionToken: %v", err)
	}
	if _, err := provider.Principal(ctx, newToken); err != nil {
		t.Fatalf("new token should be valid: %v", err)
	}
}

func TestCurrentPrincipalReloadsCurrentRole(t *testing.T) {
	provider, ctx := setup(t)

	if err := provider.CreateUser(ctx, "alice@example.com", "pass1234", RoleAdmin); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	principal, err := provider.Authenticate(ctx, Credentials{Email: "alice@example.com", Password: "pass1234"})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	var secret corev1.Secret
	if err := provider.client.Get(ctx, types.NamespacedName{
		Name:      userSecretName("alice@example.com"),
		Namespace: namespace,
	}, &secret); err != nil {
		t.Fatalf("Get user secret: %v", err)
	}
	secret.Data["role"] = []byte(RoleMember)
	if err := provider.client.Update(ctx, &secret); err != nil {
		t.Fatalf("Update user secret: %v", err)
	}

	refreshed, err := provider.CurrentPrincipal(ctx, principal.Email, principal.PasswordGen)
	if err != nil {
		t.Fatalf("CurrentPrincipal: %v", err)
	}
	if refreshed.Role != RoleMember {
		t.Fatalf("expected refreshed role %q, got %q", RoleMember, refreshed.Role)
	}
}

func TestRevokedUserInvalidatesToken(t *testing.T) {
	provider, ctx := setup(t)

	if err := provider.CreateUser(ctx, "carol@example.com", "original1", RoleMember); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	principal, err := provider.Authenticate(ctx, Credentials{Email: "carol@example.com", Password: "original1"})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	token, err := provider.GenerateSessionToken(ctx, principal)
	if err != nil {
		t.Fatalf("GenerateSessionToken: %v", err)
	}
	if err := provider.RevokeUser(ctx, "carol@example.com"); err != nil {
		t.Fatalf("RevokeUser: %v", err)
	}

	if _, err := provider.Principal(ctx, token); err == nil {
		t.Fatal("revoked user token should be rejected")
	} else if !stderrors.Is(err, ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}

func TestJWTRejectsTokenWithoutIssuerAudience(t *testing.T) {
	provider, ctx := setup(t)

	// Manually craft a token without iss/aud claims.
	key, err := provider.jwt.signingKey(ctx)
	if err != nil {
		t.Fatalf("signingKey: %v", err)
	}

	claims := jwt.MapClaims{
		"sub":     "alice@example.com",
		"email":   "alice@example.com",
		"role":    "admin",
		"pwd_gen": float64(0),
		"iat":     time.Now().Unix(),
		"exp":     time.Now().Add(time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}

	_, _, err = provider.jwt.ValidateToken(ctx, tokenString)
	if err == nil {
		t.Fatal("token without iss/aud should be rejected")
	}
}

func TestJWTRejectsTokenWithWrongIssuer(t *testing.T) {
	provider, ctx := setup(t)

	key, err := provider.jwt.signingKey(ctx)
	if err != nil {
		t.Fatalf("signingKey: %v", err)
	}

	claims := jwt.MapClaims{
		"sub":     "alice@example.com",
		"email":   "alice@example.com",
		"role":    "admin",
		"pwd_gen": float64(0),
		"iss":     "other-service",
		"aud":     "mortise-api",
		"iat":     time.Now().Unix(),
		"exp":     time.Now().Add(time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}

	_, _, err = provider.jwt.ValidateToken(ctx, tokenString)
	if err == nil {
		t.Fatal("token with wrong issuer should be rejected")
	}
}

func TestJWTRejectsTokenWithWrongAudience(t *testing.T) {
	provider, ctx := setup(t)

	key, err := provider.jwt.signingKey(ctx)
	if err != nil {
		t.Fatalf("signingKey: %v", err)
	}

	claims := jwt.MapClaims{
		"sub":     "alice@example.com",
		"email":   "alice@example.com",
		"role":    "admin",
		"pwd_gen": float64(0),
		"iss":     "mortise",
		"aud":     "other-api",
		"iat":     time.Now().Unix(),
		"exp":     time.Now().Add(time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}

	_, _, err = provider.jwt.ValidateToken(ctx, tokenString)
	if err == nil {
		t.Fatal("token with wrong audience should be rejected")
	}
}

// countBcryptCompares swaps the compare seam for one test and reports how
// many times it ran. The timing-oracle guarantee is structural — every
// Authenticate/VerifyPassword path performs exactly one bcrypt comparison —
// which a counter asserts deterministically where wall-clock timing cannot.
func countBcryptCompares(t *testing.T) *int {
	t.Helper()
	orig := compareHashAndPassword
	count := 0
	compareHashAndPassword = func(hash, password []byte) error {
		count++
		return orig(hash, password)
	}
	t.Cleanup(func() { compareHashAndPassword = orig })
	return &count
}

func TestAuthenticateTimingOracle(t *testing.T) {
	provider, ctx := setup(t)
	if err := provider.CreateUser(ctx, "alice@example.com", "s3cret12", RoleAdmin); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	cases := []struct {
		name  string
		email string
	}{
		{"existing user, wrong password", "alice@example.com"},
		{"unknown user", "nobody@example.com"},
	}
	var wantErr string
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			count := countBcryptCompares(t)
			_, err := provider.Authenticate(ctx, Credentials{Email: tc.email, Password: "wrongpwd"})
			if err == nil {
				t.Fatal("expected authentication failure")
			}
			if *count != 1 {
				t.Fatalf("expected exactly 1 bcrypt compare, got %d", *count)
			}
			if i == 0 {
				wantErr = err.Error()
			} else if err.Error() != wantErr {
				t.Fatalf("error strings differ between paths: %q vs %q", err.Error(), wantErr)
			}
		})
	}
}

func TestVerifyPasswordTimingOracle(t *testing.T) {
	provider, ctx := setup(t)
	if err := provider.CreateUser(ctx, "carol@example.com", "s3cret12", RoleMember); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	for _, email := range []string{"carol@example.com", "nobody@example.com"} {
		count := countBcryptCompares(t)
		if err := provider.VerifyPassword(ctx, email, "wrongpwd"); err == nil {
			t.Fatalf("expected failure for %s", email)
		}
		if *count != 1 {
			t.Fatalf("expected exactly 1 bcrypt compare for %s, got %d", email, *count)
		}
	}
}
