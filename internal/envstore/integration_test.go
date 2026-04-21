package envstore

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestStoreSetAndGet(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}
	ctx := context.Background()

	// Create the namespace (fake client doesn't enforce ns existence but good practice).
	ns := "pj-test-production"
	vars := []Env{
		{Name: "DATABASE_URL", Value: "postgres://...", Source: "user"},
		{Name: "JWT_SECRET", Value: "abc123", Source: "generated"},
	}
	labels := map[string]string{"mortise.dev/project": "test"}

	if err := store.Set(ctx, ns, "backend", vars, labels); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(ctx, ns, "backend")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 vars, got %d", len(got))
	}

	byName := make(map[string]Env)
	for _, e := range got {
		byName[e.Name] = e
	}
	if byName["DATABASE_URL"].Value != "postgres://..." {
		t.Errorf("DATABASE_URL = %q", byName["DATABASE_URL"].Value)
	}
	if byName["DATABASE_URL"].Source != "user" {
		t.Errorf("DATABASE_URL source = %q, want user", byName["DATABASE_URL"].Source)
	}
	if byName["JWT_SECRET"].Source != "generated" {
		t.Errorf("JWT_SECRET source = %q, want generated", byName["JWT_SECRET"].Source)
	}
}

func TestStoreMergePreservesExternalKeys(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	// Pre-create a Secret with an "external" key (simulating ESO/Vault).
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "backend-env",
			Namespace: "pj-test-production",
			Labels:    map[string]string{ManagedByLabel: ManagedByValue},
		},
		Data: map[string][]byte{
			"EXTERNAL_API_KEY": []byte("from-vault"),
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	store := &Store{Client: c}
	ctx := context.Background()

	// Merge Mortise-managed vars — should NOT wipe EXTERNAL_API_KEY.
	mortiseVars := []Env{
		{Name: "PORT", Value: "3000", Source: "user"},
	}
	if err := store.Merge(ctx, "pj-test-production", "backend", mortiseVars, nil); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(ctx, "pj-test-production", "backend")
	if err != nil {
		t.Fatal(err)
	}

	byName := make(map[string]Env)
	for _, e := range got {
		byName[e.Name] = e
	}
	if _, ok := byName["EXTERNAL_API_KEY"]; !ok {
		t.Error("EXTERNAL_API_KEY was wiped by Merge — should be preserved")
	}
	if byName["EXTERNAL_API_KEY"].Value != "from-vault" {
		t.Errorf("EXTERNAL_API_KEY value changed: %q", byName["EXTERNAL_API_KEY"].Value)
	}
	if byName["PORT"].Value != "3000" {
		t.Errorf("PORT = %q, want 3000", byName["PORT"].Value)
	}
}

func TestSharedVarsControlPlaneFlow(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}
	ctx := context.Background()

	controlNs := "pj-myproject"
	envNs := "pj-myproject-production"

	// Step 1: API writes shared vars to control namespace.
	sharedVars := []Env{
		{Name: "JWT_SECRET", Value: "shared-secret", Source: "generated"},
		{Name: "LOG_LEVEL", Value: "info", Source: "shared"},
	}
	labels := map[string]string{"mortise.dev/project": "myproject"}
	if err := store.SetSharedSource(ctx, controlNs, sharedVars, labels); err != nil {
		t.Fatal("write to control ns:", err)
	}

	// Verify it's in the control namespace.
	source, err := store.GetSharedSource(ctx, controlNs)
	if err != nil {
		t.Fatal("read from control ns:", err)
	}
	if len(source) != 2 {
		t.Fatalf("expected 2 shared vars in control ns, got %d", len(source))
	}

	// Step 2: Controller materializes into env namespace (simulating reconcile).
	if err := store.SetShared(ctx, envNs, source, labels); err != nil {
		t.Fatal("materialize to env ns:", err)
	}

	// Verify shared-env exists in env namespace.
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: envNs, Name: SharedEnvName}, &secret); err != nil {
		t.Fatal("shared-env not found in env ns:", err)
	}
	if string(secret.Data["JWT_SECRET"]) != "shared-secret" {
		t.Errorf("JWT_SECRET in shared-env = %q", string(secret.Data["JWT_SECRET"]))
	}

	// Step 3: Verify env namespace has shared-env but control ns has shared-vars.
	var controlSecret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: controlNs, Name: SharedVarsSourceName}, &controlSecret); err != nil {
		t.Fatal("shared-vars not found in control ns:", err)
	}
}

func TestSharedVarsMergeSourcePreservesExisting(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}
	ctx := context.Background()

	controlNs := "pj-test"
	labels := map[string]string{"mortise.dev/project": "test"}

	// Write initial vars.
	initial := []Env{
		{Name: "JWT_SECRET", Value: "original", Source: "generated"},
	}
	if err := store.SetSharedSource(ctx, controlNs, initial, labels); err != nil {
		t.Fatal(err)
	}

	// Merge additional vars — should not wipe JWT_SECRET.
	additional := []Env{
		{Name: "LOG_LEVEL", Value: "debug", Source: "shared"},
	}
	if err := store.MergeSharedSource(ctx, controlNs, additional, labels); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetSharedSource(ctx, controlNs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 vars after merge, got %d", len(got))
	}

	byName := make(map[string]Env)
	for _, e := range got {
		byName[e.Name] = e
	}
	if byName["JWT_SECRET"].Value != "original" {
		t.Errorf("JWT_SECRET was overwritten: %q", byName["JWT_SECRET"].Value)
	}
	if byName["LOG_LEVEL"].Value != "debug" {
		t.Errorf("LOG_LEVEL = %q, want debug", byName["LOG_LEVEL"].Value)
	}
}

func TestDeleteVar(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}
	ctx := context.Background()

	ns := "pj-test-production"
	vars := []Env{
		{Name: "A", Value: "1", Source: "user"},
		{Name: "B", Value: "2", Source: "binding"},
	}
	_ = store.Set(ctx, ns, "app", vars, nil)

	if err := store.Delete(ctx, ns, "app", "A"); err != nil {
		t.Fatal(err)
	}

	got, _ := store.Get(ctx, ns, "app")
	if len(got) != 1 {
		t.Fatalf("expected 1 var after delete, got %d", len(got))
	}
	if got[0].Name != "B" {
		t.Errorf("remaining var = %q, want B", got[0].Name)
	}
}

func TestEnvFromSourcesOptional(t *testing.T) {
	sources := EnvFromSources("myapp")
	for _, s := range sources {
		if s.SecretRef.Optional == nil || !*s.SecretRef.Optional {
			t.Errorf("envFrom source %q should be optional", s.SecretRef.Name)
		}
	}
}

func TestGetNonExistentReturnsNil(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}

	got, err := store.Get(context.Background(), "no-ns", "no-app")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil for non-existent, got %v", got)
	}
}
