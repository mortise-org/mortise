package auth

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// A user created a moment ago may not be in the cache yet; authentication
// must fall back to an uncached read before calling the credentials invalid.
func TestAuthenticateFallsBackToUncachedReader(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	apiserver := fake.NewClientBuilder().WithScheme(scheme).Build() // has the user
	if err := NewNativeAuthProvider(apiserver).CreateUser(ctx, "new@example.com", "correct horse", RoleMember); err != nil {
		t.Fatal(err)
	}
	cache := fake.NewClientBuilder().WithScheme(scheme).Build() // lagging: empty

	if _, err := NewNativeAuthProvider(cache).Authenticate(ctx, Credentials{Email: "new@example.com", Password: "correct horse"}); err == nil {
		t.Fatal("without a fallback the cache miss must fail (this is the race)")
	}
	withFallback := NewNativeAuthProvider(cache).WithUncachedReader(apiserver)
	if _, err := withFallback.Authenticate(ctx, Credentials{Email: "new@example.com", Password: "correct horse"}); err != nil {
		t.Fatalf("fallback read must authenticate the just-created user: %v", err)
	}
	if _, err := withFallback.Authenticate(ctx, Credentials{Email: "nobody@example.com", Password: "x"}); err == nil {
		t.Fatal("an unknown user must still fail")
	}
}
