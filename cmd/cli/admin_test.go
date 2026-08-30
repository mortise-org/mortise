package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/mortise-org/mortise/internal/auth"
)

func TestAdminCreateUserAndResetPassword(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	var out bytes.Buffer

	if err := adminCreateUser(ctx, c, &out, "ops@example.com", "correct horse", auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	p := auth.NewNativeAuthProvider(c)
	if _, err := p.Authenticate(ctx, auth.Credentials{Email: "ops@example.com", Password: "correct horse"}); err != nil {
		t.Fatalf("login with the created password: %v", err)
	}

	// A token minted before the reset carries the old password generation.
	before, err := p.Authenticate(ctx, auth.Credentials{Email: "ops@example.com", Password: "correct horse"})
	if err != nil {
		t.Fatal(err)
	}

	if err := adminResetPassword(ctx, c, &out, "ops@example.com", "battery staple"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Authenticate(ctx, auth.Credentials{Email: "ops@example.com", Password: "correct horse"}); err == nil {
		t.Fatal("old password still authenticates")
	}
	after, err := p.Authenticate(ctx, auth.Credentials{Email: "ops@example.com", Password: "battery staple"})
	if err != nil {
		t.Fatalf("login with the new password: %v", err)
	}
	if after.PasswordGen <= before.PasswordGen {
		t.Fatalf("password generation must advance so old tokens stop validating: before=%d after=%d", before.PasswordGen, after.PasswordGen)
	}
	if !strings.Contains(out.String(), "previously issued tokens are no longer valid") {
		t.Fatalf("output: %q", out.String())
	}
}

func TestAdminResetPasswordUnknownUser(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	var out bytes.Buffer
	if err := adminResetPassword(context.Background(), c, &out, "nobody@example.com", "long enough"); err == nil {
		t.Fatal("expected an error for an unknown user")
	}
}

func TestReadPasswordFromStdin(t *testing.T) {
	pw, err := readPassword(strings.NewReader("s3cret-line\nsecond line\n"), &bytes.Buffer{}, true, "")
	if err != nil || pw != "s3cret-line" {
		t.Fatalf("got %q, %v", pw, err)
	}
	if _, err := readPassword(strings.NewReader("\n"), &bytes.Buffer{}, true, ""); err == nil {
		t.Fatal("empty stdin password must be rejected")
	}
}
