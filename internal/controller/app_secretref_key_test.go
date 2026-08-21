package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// Mortise's valueFrom.secretRef is a bare Secret name whose keys must equal the
// env var names -- the opposite of core-v1's explicit secretKeyRef (CAI-156).
// So the natural mistakes all produce a missing key, and a missing key used to
// resolve to an empty string with a nil error: an empty credential in the
// container, and an App reporting Ready (CAI-162).
func TestResolveEnvVarValue_SecretRefKey(t *testing.T) {
	const envNs = "pj-demo-production"
	scheme := gcTestScheme(t)

	build := func(data map[string][]byte) *AppReconciler {
		sec := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "app-credentials", Namespace: envNs},
			Data:       data,
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sec).Build()
		return &AppReconciler{Client: c, Scheme: scheme}
	}

	ref := func(name string) mortisev1alpha1.EnvVar {
		return mortisev1alpha1.EnvVar{
			Name:      name,
			ValueFrom: &mortisev1alpha1.EnvVarSource{SecretRef: "app-credentials"},
		}
	}

	t.Run("resolves a present key", func(t *testing.T) {
		r := build(map[string][]byte{"API_KEY": []byte("s3cret")})
		got, source, err := r.resolveEnvVarValue(
			context.Background(), ref("API_KEY"), envNs, "demo", "production", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "s3cret" || source != "user" {
			t.Errorf("expected s3cret/user, got %q/%q", got, source)
		}
	})

	t.Run("errors when the key is absent instead of returning empty", func(t *testing.T) {
		r := build(map[string][]byte{"SOME_OTHER_KEY": []byte("x")})
		got, _, err := r.resolveEnvVarValue(
			context.Background(), ref("API_KEY"), envNs, "demo", "production", nil)
		if err == nil {
			t.Fatalf("expected an error for a missing key, got value %q and nil error", got)
		}
		// The message has to name both the Secret and the key, because the
		// whole trap is that the key name is implicit.
		for _, want := range []string{"API_KEY", "app-credentials", envNs} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error should mention %q, got: %v", want, err)
			}
		}
	})

	// Absent and empty are different, and only absent is a mistake the
	// platform can detect. An operator who deliberately sets an empty value
	// should keep getting one.
	t.Run("an explicitly empty value stays legal", func(t *testing.T) {
		r := build(map[string][]byte{"API_KEY": []byte("")})
		got, _, err := r.resolveEnvVarValue(
			context.Background(), ref("API_KEY"), envNs, "demo", "production", nil)
		if err != nil {
			t.Fatalf("an explicitly empty value should resolve, got: %v", err)
		}
		if got != "" {
			t.Errorf("expected an empty value, got %q", got)
		}
	})

	t.Run("a missing Secret still errors", func(t *testing.T) {
		r := build(map[string][]byte{"API_KEY": []byte("x")})
		ev := mortisev1alpha1.EnvVar{
			Name:      "API_KEY",
			ValueFrom: &mortisev1alpha1.EnvVarSource{SecretRef: "does-not-exist"},
		}
		if _, _, err := r.resolveEnvVarValue(
			context.Background(), ev, envNs, "demo", "production", nil); err == nil {
			t.Error("expected an error for a missing Secret")
		}
	})
}
