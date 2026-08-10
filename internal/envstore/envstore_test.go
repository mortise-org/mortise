package envstore

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type countingClient struct {
	client.Client
	updateCalls int
}

func (c *countingClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.updateCalls++
	return c.Client.Update(ctx, obj, opts...)
}

func TestAppEnvSecretName(t *testing.T) {
	tests := []struct {
		app  string
		want string
	}{
		{"backend", "backend-env"},
		{"supabase-postgres", "supabase-postgres-env"},
	}
	for _, tt := range tests {
		if got := AppEnvSecretName(tt.app); got != tt.want {
			t.Errorf("AppEnvSecretName(%q) = %q, want %q", tt.app, got, tt.want)
		}
	}
}

func TestEnvFromSources(t *testing.T) {
	sources := EnvFromSources("backend")
	if len(sources) != 2 {
		t.Fatalf("expected 2 envFrom sources, got %d", len(sources))
	}
	if sources[0].SecretRef.Name != SharedEnvName {
		t.Errorf("first source should be shared-env, got %q", sources[0].SecretRef.Name)
	}
	if sources[1].SecretRef.Name != "backend-env" {
		t.Errorf("second source should be backend-env, got %q", sources[1].SecretRef.Name)
	}
	// Both should be optional
	if sources[0].SecretRef.Optional == nil || !*sources[0].SecretRef.Optional {
		t.Error("shared-env should be optional")
	}
	if sources[1].SecretRef.Optional == nil || !*sources[1].SecretRef.Optional {
		t.Error("app-env should be optional")
	}
}

func TestBuildSecretSourceAnnotations(t *testing.T) {
	vars := []Env{
		{Name: "DATABASE_URL", Value: "postgres://...", Source: "binding"},
		{Name: "JWT_SECRET", Value: "abc123", Source: "generated"},
		{Name: "PORT", Value: "3000", Source: "user"},
		{Name: "SHARED_KEY", Value: "xyz", Source: "shared"},
	}
	secret := buildSecret("test-ns", "test-env", vars, nil)

	if secret.Annotations[AnnotationBindingKeys] != "DATABASE_URL" {
		t.Errorf("binding keys = %q, want DATABASE_URL", secret.Annotations[AnnotationBindingKeys])
	}
	if secret.Annotations[AnnotationGeneratedKeys] != "JWT_SECRET" {
		t.Errorf("generated keys = %q, want JWT_SECRET", secret.Annotations[AnnotationGeneratedKeys])
	}
	if secret.Annotations[AnnotationSharedKeys] != "SHARED_KEY" {
		t.Errorf("shared keys = %q, want SHARED_KEY", secret.Annotations[AnnotationSharedKeys])
	}
	// "user" source should not appear in any annotation
	for _, ann := range []string{AnnotationBindingKeys, AnnotationGeneratedKeys, AnnotationSharedKeys} {
		if val, ok := secret.Annotations[ann]; ok && val == "PORT" {
			t.Errorf("user-sourced key PORT should not appear in %s", ann)
		}
	}
}

func TestSecretToEnvsRoundTrip(t *testing.T) {
	vars := []Env{
		{Name: "A", Value: "1", Source: "binding"},
		{Name: "B", Value: "2", Source: "generated"},
		{Name: "C", Value: "3", Source: "user"},
	}
	secret := buildSecret("ns", "name", vars, nil)
	got := secretToEnvs(secret)

	if len(got) != 3 {
		t.Fatalf("expected 3 envs, got %d", len(got))
	}

	byName := make(map[string]Env)
	for _, e := range got {
		byName[e.Name] = e
	}

	if byName["A"].Source != "binding" {
		t.Errorf("A source = %q, want binding", byName["A"].Source)
	}
	if byName["B"].Source != "generated" {
		t.Errorf("B source = %q, want generated", byName["B"].Source)
	}
	if byName["C"].Source != "user" {
		t.Errorf("C source = %q, want user", byName["C"].Source)
	}
}

func TestParseKeySet(t *testing.T) {
	got := parseKeySet("A,B,C")
	if !got["A"] || !got["B"] || !got["C"] {
		t.Errorf("expected A,B,C in set, got %v", got)
	}
	if got := parseKeySet(""); got != nil {
		t.Errorf("empty string should return nil, got %v", got)
	}
}

func TestRemoveKeyFromAnnotations(t *testing.T) {
	secret := buildSecret("ns", "name", []Env{
		{Name: "A", Value: "1", Source: "binding"},
		{Name: "B", Value: "2", Source: "binding"},
	}, nil)

	removeKeyFromAnnotations(secret, "A")

	if secret.Annotations[AnnotationBindingKeys] != "B" {
		t.Errorf("after removing A, binding keys = %q, want B", secret.Annotations[AnnotationBindingKeys])
	}

	removeKeyFromAnnotations(secret, "B")
	if _, ok := secret.Annotations[AnnotationBindingKeys]; ok {
		t.Error("after removing B, binding keys annotation should be deleted")
	}
}

func TestBuildSecretLabels(t *testing.T) {
	extra := map[string]string{
		"mortise.dev/project": "supabase",
	}
	secret := buildSecret("ns", "name", nil, extra)

	if secret.Labels[ManagedByLabel] != ManagedByValue {
		t.Error("missing managed-by label")
	}
	if secret.Labels["mortise.dev/project"] != "supabase" {
		t.Error("missing extra label")
	}
}

func TestBuildSecretDataEncoding(t *testing.T) {
	vars := []Env{
		{Name: "PASSWORD", Value: "s3cret!@#$"},
	}
	secret := buildSecret("ns", "name", vars, nil)

	if string(secret.Data["PASSWORD"]) != "s3cret!@#$" {
		t.Errorf("password roundtrip failed: got %q", string(secret.Data["PASSWORD"]))
	}
}

func TestStoreMergeRetriesOnConflict(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	existing := buildSecret("ns", AppEnvSecretName("app"), []Env{{Name: "A", Value: "1", Source: "user"}}, nil)
	updateCalls := 0
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).WithInterceptorFuncs(interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			updateCalls++
			if updateCalls == 1 {
				return apierrors.NewConflict(schema.GroupResource{Group: "", Resource: "secrets"}, obj.GetName(), errors.New("conflict"))
			}
			return c.Update(ctx, obj, opts...)
		},
	}).Build()

	store := &Store{Client: c}
	if err := store.Merge(context.Background(), "ns", "app", []Env{{Name: "B", Value: "2", Source: "generated"}}, nil); err != nil {
		t.Fatal(err)
	}
	if updateCalls < 2 {
		t.Fatalf("expected retry after conflict, got %d update calls", updateCalls)
	}

	got, err := store.Get(context.Background(), "ns", "app")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 vars after merge, got %d", len(got))
	}
}

func TestDeleteVarRetriesOnConflict(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	existing := buildSecret("ns", AppEnvSecretName("app"), []Env{
		{Name: "A", Value: "1", Source: "user"},
		{Name: "B", Value: "2", Source: "binding"},
	}, nil)
	updateCalls := 0
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).WithInterceptorFuncs(interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			updateCalls++
			if updateCalls == 1 {
				return apierrors.NewConflict(schema.GroupResource{Group: "", Resource: "secrets"}, obj.GetName(), errors.New("conflict"))
			}
			return c.Update(ctx, obj, opts...)
		},
	}).Build()

	store := &Store{Client: c}
	if err := store.Delete(context.Background(), "ns", "app", "A"); err != nil {
		t.Fatal(err)
	}
	if updateCalls < 2 {
		t.Fatalf("expected retry after conflict, got %d update calls", updateCalls)
	}

	var secret corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Name: AppEnvSecretName("app"), Namespace: "ns"}, &secret); err != nil {
		t.Fatal(err)
	}
	if _, ok := secret.Data["A"]; ok {
		t.Fatal("expected A to be deleted")
	}
	if secret.Annotations[AnnotationBindingKeys] != "B" {
		t.Fatalf("binding annotation = %q, want B", secret.Annotations[AnnotationBindingKeys])
	}
}

func TestApplyConcurrentWritersBothSurvive(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	existing := buildSecret("ns", AppEnvSecretName("app"), []Env{{Name: "FOO", Value: "1", Source: "user"}}, nil)
	updateCalls := 0
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).WithInterceptorFuncs(interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			updateCalls++
			if updateCalls == 1 {
				// Simulate a second Apply landing between this writer's read
				// and its update, then fail the stale update with a conflict.
				competing := &Store{Client: c}
				if err := competing.Apply(ctx, "ns", "app", nil, func(current []Env) ([]Env, error) {
					return append(current, Env{Name: "BAZ", Value: "3", Source: "user"}), nil
				}); err != nil {
					return err
				}
				return apierrors.NewConflict(schema.GroupResource{Group: "", Resource: "secrets"}, obj.GetName(), errors.New("conflict"))
			}
			return c.Update(ctx, obj, opts...)
		},
	}).Build()

	store := &Store{Client: c}
	err := store.Apply(context.Background(), "ns", "app", nil, func(current []Env) ([]Env, error) {
		return append(current, Env{Name: "BAR", Value: "2", Source: "user"}), nil
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(context.Background(), "ns", "app")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range got {
		names[e.Name] = true
	}
	for _, want := range []string{"FOO", "BAR", "BAZ"} {
		if !names[want] {
			t.Errorf("expected %s to survive concurrent Apply calls, got %v", want, got)
		}
	}
}

func TestApplyCreatesWhenMissing(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	store := &Store{Client: c}
	err := store.Apply(context.Background(), "ns", "app", nil, func(current []Env) ([]Env, error) {
		if current != nil {
			t.Errorf("expected nil current for missing secret, got %v", current)
		}
		return []Env{{Name: "A", Value: "1", Source: "user"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(context.Background(), "ns", "app")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "A" {
		t.Fatalf("expected [A], got %v", got)
	}
}

func TestApplyErrSkipDoesNotCreateSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	store := &Store{Client: c}
	err := store.Apply(context.Background(), "ns", "app", nil, func(current []Env) ([]Env, error) {
		return nil, ErrSkip
	})
	if err != nil {
		t.Fatal(err)
	}

	exists, err := store.SecretExists(context.Background(), "ns", "app")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("ErrSkip should not create the secret")
	}
}

func TestApplyRetriesAsUpdateOnLostCreateRace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	createCalls := 0
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			createCalls++
			if createCalls == 1 {
				// Simulate another writer creating the secret first.
				winner := buildSecret("ns", AppEnvSecretName("app"), []Env{{Name: "WINNER", Value: "1", Source: "user"}}, nil)
				if err := c.Create(ctx, winner); err != nil {
					return err
				}
				return apierrors.NewAlreadyExists(schema.GroupResource{Group: "", Resource: "secrets"}, obj.GetName())
			}
			return c.Create(ctx, obj, opts...)
		},
	}).Build()

	store := &Store{Client: c}
	err := store.Apply(context.Background(), "ns", "app", nil, func(current []Env) ([]Env, error) {
		return append(current, Env{Name: "LOSER", Value: "2", Source: "user"}), nil
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(context.Background(), "ns", "app")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range got {
		names[e.Name] = true
	}
	if !names["WINNER"] || !names["LOSER"] {
		t.Fatalf("expected both writers' vars after create race, got %v", got)
	}
}

func TestUpdateWithConflictRetryStopsWhenUnchanged(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	err := UpdateWithConflictRetry(context.Background(), c, client.ObjectKey{Name: "s", Namespace: "ns"}, func() *corev1.Secret {
		return &corev1.Secret{}
	}, func(secret *corev1.Secret) (bool, error) {
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReplaceSourceSkipsEmptySecretDataNilVsEmptyChurn(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	existing := buildSecret("ns", AppEnvSecretName("app"), nil, nil)
	existing.Data = nil
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	c := &countingClient{Client: base}

	store := &Store{Client: c}
	if err := store.ReplaceSource(context.Background(), "ns", "app", "binding", nil, nil); err != nil {
		t.Fatal(err)
	}
	if c.updateCalls != 0 {
		t.Fatalf("expected no update for logically empty secret data, got %d update calls", c.updateCalls)
	}
}
