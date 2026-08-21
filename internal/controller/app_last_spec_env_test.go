package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/mortise-org/mortise/internal/envstore"
)

// The last-spec-env annotation exists to answer one question: does the live
// value still match what the spec last set? It answered it by storing the
// value, which put resolved credentials in plaintext on the derived Secret --
// including ones deliberately moved out of the CRD into a Secret (CAI-168).
// A digest answers the same question without keeping the credential.
func TestLastSpecEnvDigest(t *testing.T) {
	const secret = "sk_live_do_not_persist_me"

	t.Run("never returns the value", func(t *testing.T) {
		got := lastSpecEnvDigest(secret)
		if strings.Contains(got, secret) || got == secret {
			t.Fatal("the digest contains the value")
		}
		if len(got) != 64 {
			t.Errorf("expected a 64-char sha256 hex digest, got %d chars", len(got))
		}
	})

	t.Run("is the real SHA-256 of the value", func(t *testing.T) {
		// A known vector rather than comparing the function to itself, which
		// asserts nothing about what it computes.
		if got, want := lastSpecEnvDigest("hunter2"),
			"f52fbd32b2b3b86ff88ef6c490628285f482af15ddcb29541f94bcf526a3f6c7"; got != want {
			t.Errorf("expected %s, got %s", want, got)
		}
	})

	t.Run("different values do not collide", func(t *testing.T) {
		if lastSpecEnvDigest(secret) == lastSpecEnvDigest(secret+"x") {
			t.Error("different values produced the same digest")
		}
	})
}

func TestLastSpecEnvAnnotation(t *testing.T) {
	scheme := gcTestScheme(t)
	const ns = "pj-demo-production"
	const app = "demo"
	const plaintext = "sk_live_abc123_plaintext"
	key := types.NamespacedName{Name: envstore.AppEnvSecretName(app), Namespace: ns}

	newSecret := func(annotations map[string]string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Annotations: annotations},
			Data:       map[string][]byte{"API_KEY": []byte(plaintext)},
		}
	}

	t.Run("writes digests, never values", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newSecret(nil)).Build()
		r := &AppReconciler{Client: c, Scheme: scheme}
		if err := r.writeLastSpecEnv(context.Background(), ns, app,
			map[string]string{"API_KEY": plaintext}); err != nil {
			t.Fatalf("write: %v", err)
		}
		var got corev1.Secret
		if err := c.Get(context.Background(), key, &got); err != nil {
			t.Fatalf("get: %v", err)
		}
		raw := got.Annotations[envstore.AnnotationLastSpecEnvDigest]
		if raw == "" {
			t.Fatal("digest annotation was not written")
		}
		if strings.Contains(raw, plaintext) {
			t.Error("the plaintext value is in the annotation")
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("annotation is not JSON: %v", err)
		}
		if m["API_KEY"] != lastSpecEnvDigest(plaintext) {
			t.Error("stored digest does not match the value's digest")
		}
	})

	// The legacy annotation is where the plaintext lived, so removing it is
	// the fix, not housekeeping.
	t.Run("deletes the legacy plaintext annotation", func(t *testing.T) {
		legacy, _ := json.Marshal(map[string]string{"API_KEY": plaintext})
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			newSecret(map[string]string{envstore.AnnotationLastSpecEnv: string(legacy)})).Build()
		r := &AppReconciler{Client: c, Scheme: scheme}
		if err := r.writeLastSpecEnv(context.Background(), ns, app,
			map[string]string{"API_KEY": plaintext}); err != nil {
			t.Fatalf("write: %v", err)
		}
		var got corev1.Secret
		if err := c.Get(context.Background(), key, &got); err != nil {
			t.Fatalf("get: %v", err)
		}
		if _, still := got.Annotations[envstore.AnnotationLastSpecEnv]; still {
			t.Error("the legacy plaintext annotation survived")
		}
		for k, v := range got.Annotations {
			if strings.Contains(v, plaintext) {
				t.Errorf("plaintext still present in annotation %q", k)
			}
		}
	})

	// Without this, the first reconcile after upgrade would see every key as
	// user-overridden and silently stop propagating spec changes for all of
	// them -- a far worse failure than the leak being fixed.
	t.Run("migrates by hashing the legacy values, so comparisons hold immediately", func(t *testing.T) {
		legacy, _ := json.Marshal(map[string]string{"API_KEY": plaintext})
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			newSecret(map[string]string{envstore.AnnotationLastSpecEnv: string(legacy)})).Build()
		r := &AppReconciler{Client: c, Scheme: scheme}

		got := r.readLastSpecEnv(context.Background(), ns, app)
		if got["API_KEY"] != lastSpecEnvDigest(plaintext) {
			t.Fatalf("legacy value was not migrated to its digest: %v", got["API_KEY"])
		}
	})

	t.Run("prefers the digest annotation when both are present", func(t *testing.T) {
		legacy, _ := json.Marshal(map[string]string{"API_KEY": "stale-value"})
		digests, _ := json.Marshal(map[string]string{"API_KEY": lastSpecEnvDigest(plaintext)})
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newSecret(map[string]string{
			envstore.AnnotationLastSpecEnv:       string(legacy),
			envstore.AnnotationLastSpecEnvDigest: string(digests),
		})).Build()
		r := &AppReconciler{Client: c, Scheme: scheme}
		if got := r.readLastSpecEnv(context.Background(), ns, app); got["API_KEY"] != lastSpecEnvDigest(plaintext) {
			t.Error("the stale legacy annotation won over the digest annotation")
		}
	})

	t.Run("no annotation at all reports nothing tracked", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newSecret(nil)).Build()
		r := &AppReconciler{Client: c, Scheme: scheme}
		if got := r.readLastSpecEnv(context.Background(), ns, app); len(got) != 0 {
			t.Errorf("expected nothing tracked, got %v", got)
		}
	})
}
