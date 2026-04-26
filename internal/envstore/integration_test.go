package envstore

import (
	"context"
	"strings"
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

func TestReplaceSource_ReplacesOnlyNamedSource(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}
	ctx := context.Background()
	ns := "pj-test-production"

	// Seed with binding + user vars.
	initial := []Env{
		{Name: "DB_HOST", Value: "old-host", Source: "binding"},
		{Name: "DB_PORT", Value: "5432", Source: "binding"},
		{Name: "USER_VAR", Value: "keep-me", Source: "user"},
	}
	if err := store.Set(ctx, ns, "app", initial, nil); err != nil {
		t.Fatal(err)
	}

	// Replace binding source with new vars.
	newBindings := []Env{
		{Name: "DB_HOST", Value: "new-host", Source: "binding"},
	}
	if err := store.ReplaceSource(ctx, ns, "app", "binding", newBindings, nil); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(ctx, ns, "app")
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]Env)
	for _, e := range got {
		byName[e.Name] = e
	}

	// Old binding var DB_PORT should be gone.
	if _, ok := byName["DB_PORT"]; ok {
		t.Error("DB_PORT should have been removed by ReplaceSource")
	}
	// New binding var should be present.
	if byName["DB_HOST"].Value != "new-host" {
		t.Errorf("DB_HOST = %q, want new-host", byName["DB_HOST"].Value)
	}
	// User var must be preserved.
	if byName["USER_VAR"].Value != "keep-me" {
		t.Errorf("USER_VAR = %q, want keep-me", byName["USER_VAR"].Value)
	}
}

func TestReplaceSource_EmptyVarsClearsBoundSource(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}
	ctx := context.Background()
	ns := "pj-test-production"

	initial := []Env{
		{Name: "DB_HOST", Value: "host", Source: "binding"},
		{Name: "MY_VAR", Value: "preserve", Source: "user"},
	}
	if err := store.Set(ctx, ns, "app", initial, nil); err != nil {
		t.Fatal(err)
	}

	// Replace binding with empty list — removes all binding vars.
	if err := store.ReplaceSource(ctx, ns, "app", "binding", nil, nil); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(ctx, ns, "app")
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]Env)
	for _, e := range got {
		byName[e.Name] = e
	}
	if _, ok := byName["DB_HOST"]; ok {
		t.Error("DB_HOST should have been cleared by empty ReplaceSource")
	}
	if byName["MY_VAR"].Value != "preserve" {
		t.Errorf("MY_VAR = %q, want preserve", byName["MY_VAR"].Value)
	}
}

func TestReplaceSource_OnNonExistentCreates(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}
	ctx := context.Background()

	vars := []Env{{Name: "DB_HOST", Value: "host", Source: "binding"}}
	if err := store.ReplaceSource(ctx, "pj-new-production", "app", "binding", vars, nil); err != nil {
		t.Fatalf("ReplaceSource on non-existent: %v", err)
	}

	got, err := store.Get(ctx, "pj-new-production", "app")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "DB_HOST" {
		t.Errorf("expected [DB_HOST], got %v", got)
	}
}

func TestSetReplacesAllData(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}
	ctx := context.Background()
	ns := "pj-test-production"

	// First Set with A and B.
	if err := store.Set(ctx, ns, "app", []Env{
		{Name: "A", Value: "1", Source: "user"},
		{Name: "B", Value: "2", Source: "user"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	// Second Set with only C — should replace A and B entirely.
	if err := store.Set(ctx, ns, "app", []Env{
		{Name: "C", Value: "3", Source: "user"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(ctx, ns, "app")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 var after Set replace, got %d: %v", len(got), got)
	}
	if got[0].Name != "C" {
		t.Errorf("expected C, got %q", got[0].Name)
	}
}

func TestSetPreservesNonMortiseAnnotations(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	// Pre-create a Secret with a non-Mortise annotation.
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-env",
			Namespace: "pj-test-production",
			Labels:    map[string]string{ManagedByLabel: ManagedByValue},
			Annotations: map[string]string{
				"linkerd.io/inject": "enabled",
			},
		},
		Data: map[string][]byte{},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	store := &Store{Client: c}
	ctx := context.Background()

	if err := store.Set(ctx, "pj-test-production", "app", []Env{
		{Name: "PORT", Value: "3000", Source: "user"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	var sec corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: "pj-test-production", Name: "app-env"}, &sec); err != nil {
		t.Fatal(err)
	}
	if sec.Annotations["linkerd.io/inject"] != "enabled" {
		t.Errorf("non-Mortise annotation was wiped; want enabled, got %q", sec.Annotations["linkerd.io/inject"])
	}
}

func TestDeleteCleansBindingAnnotation(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}
	ctx := context.Background()
	ns := "pj-test-production"

	if err := store.Set(ctx, ns, "app", []Env{
		{Name: "DB_HOST", Value: "host", Source: "binding"},
		{Name: "DB_PORT", Value: "5432", Source: "binding"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(ctx, ns, "app", "DB_HOST"); err != nil {
		t.Fatal(err)
	}

	var sec corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "app-env"}, &sec); err != nil {
		t.Fatal(err)
	}
	csv := sec.Annotations[AnnotationBindingKeys]
	for _, k := range []string{"DB_HOST"} {
		for _, part := range splitCSV(csv) {
			if part == k {
				t.Errorf("deleted key %q still present in binding annotation: %q", k, csv)
			}
		}
	}
	// DB_PORT should still be in the annotation.
	found := false
	for _, part := range splitCSV(csv) {
		if part == "DB_PORT" {
			found = true
		}
	}
	if !found {
		t.Errorf("DB_PORT should still be in binding annotation, got %q", csv)
	}
}

func TestDeleteRemovesAnnotationKeyWhenLastEntry(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}
	ctx := context.Background()
	ns := "pj-test-production"

	if err := store.Set(ctx, ns, "app", []Env{
		{Name: "ONLY_BINDING", Value: "v", Source: "binding"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(ctx, ns, "app", "ONLY_BINDING"); err != nil {
		t.Fatal(err)
	}

	var sec corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "app-env"}, &sec); err != nil {
		t.Fatal(err)
	}
	if _, ok := sec.Annotations[AnnotationBindingKeys]; ok {
		t.Errorf("binding annotation should be removed entirely when last key deleted, got %q", sec.Annotations[AnnotationBindingKeys])
	}
}

func TestDeleteGeneratedAnnotationCleanup(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}
	ctx := context.Background()
	ns := "pj-test-production"

	if err := store.Set(ctx, ns, "app", []Env{
		{Name: "JWT_SECRET", Value: "abc", Source: "generated"},
		{Name: "PORT", Value: "3000", Source: "user"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(ctx, ns, "app", "JWT_SECRET"); err != nil {
		t.Fatal(err)
	}

	var sec corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "app-env"}, &sec); err != nil {
		t.Fatal(err)
	}
	if _, ok := sec.Annotations[AnnotationGeneratedKeys]; ok {
		t.Errorf("generated annotation should be removed when last key deleted, got %q", sec.Annotations[AnnotationGeneratedKeys])
	}
	// User var data should still be present.
	if string(sec.Data["PORT"]) != "3000" {
		t.Errorf("PORT data = %q, want 3000", string(sec.Data["PORT"]))
	}
}

func TestSecretExistsReturnsTrue(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}
	ctx := context.Background()
	ns := "pj-test-production"

	if err := store.Set(ctx, ns, "app", []Env{{Name: "A", Value: "1", Source: "user"}}, nil); err != nil {
		t.Fatal(err)
	}

	exists, err := store.SecretExists(ctx, ns, "app")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("SecretExists should return true after Set")
	}
}

func TestSecretExistsReturnsFalse(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}

	exists, err := store.SecretExists(context.Background(), "no-ns", "no-app")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("SecretExists should return false for non-existent Secret")
	}
}

func TestEnsureExistsCreatesEmptySecret(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}
	ctx := context.Background()
	ns := "pj-test-production"

	if err := store.EnsureExists(ctx, ns, "app", map[string]string{"mortise.dev/project": "test"}); err != nil {
		t.Fatal(err)
	}

	var sec corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "app-env"}, &sec); err != nil {
		t.Fatalf("Secret should exist after EnsureExists: %v", err)
	}
	if len(sec.Data) != 0 {
		t.Errorf("expected empty Secret, got %v", sec.Data)
	}
}

func TestEnsureExistsIsNoopWhenPresent(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}
	ctx := context.Background()
	ns := "pj-test-production"

	// Set data first.
	if err := store.Set(ctx, ns, "app", []Env{{Name: "A", Value: "1", Source: "user"}}, nil); err != nil {
		t.Fatal(err)
	}

	// EnsureExists should not overwrite.
	if err := store.EnsureExists(ctx, ns, "app", nil); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(ctx, ns, "app")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "A" {
		t.Errorf("EnsureExists wiped existing data, got %v", got)
	}
}

func TestMergeOnNonExistentCreates(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}
	ctx := context.Background()

	if err := store.Merge(ctx, "pj-new-production", "app", []Env{
		{Name: "DB_URL", Value: "postgres://...", Source: "user"},
	}, nil); err != nil {
		t.Fatalf("Merge on non-existent: %v", err)
	}

	got, err := store.Get(ctx, "pj-new-production", "app")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "DB_URL" {
		t.Errorf("expected [DB_URL], got %v", got)
	}
}

func TestValidateEnvVarNameRejectsInvalid(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"VALID_NAME", false},
		{"_valid", false},
		{"has space", true},
		{"has,comma", true},
		{"has-dash", true},
		{"1starts_with_digit", true},
		{"", true},
		{"has.dot", true},
	}
	for _, tc := range cases {
		err := ValidateEnvVarName(tc.name)
		if tc.wantErr && err == nil {
			t.Errorf("ValidateEnvVarName(%q) = nil, want error", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("ValidateEnvVarName(%q) = %v, want nil", tc.name, err)
		}
	}
}

func TestDeleteOnNonExistentIsNoop(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}

	if err := store.Delete(context.Background(), "pj-no-ns", "no-app", "KEY"); err != nil {
		t.Errorf("Delete on non-existent should return nil, got %v", err)
	}
}

func TestGetSharedSourceNonExistentReturnsNil(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}

	got, err := store.GetSharedSource(context.Background(), "pj-no-project")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil for non-existent shared source, got %v", got)
	}
}

func TestEnsureSharedExistsCreatesSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}
	ctx := context.Background()
	ns := "pj-test-production"

	if err := store.EnsureSharedExists(ctx, ns, map[string]string{"mortise.dev/project": "test"}); err != nil {
		t.Fatal(err)
	}

	var sec corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: SharedEnvName}, &sec); err != nil {
		t.Fatalf("shared-env Secret should exist after EnsureSharedExists: %v", err)
	}
}

func TestEnsureSharedExistsIsNoopWhenPresent(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}
	ctx := context.Background()
	ns := "pj-test-production"

	if err := store.SetShared(ctx, ns, []Env{{Name: "A", Value: "1", Source: "shared"}}, nil); err != nil {
		t.Fatal(err)
	}

	if err := store.EnsureSharedExists(ctx, ns, nil); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetShared(ctx, ns)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "A" {
		t.Errorf("EnsureSharedExists wiped existing shared data, got %v", got)
	}
}

func TestSourceAnnotationsTrackedCorrectly(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}
	ctx := context.Background()
	ns := "pj-test-production"

	if err := store.Set(ctx, ns, "app", []Env{
		{Name: "DB_HOST", Value: "host", Source: "binding"},
		{Name: "JWT_SECRET", Value: "abc", Source: "generated"},
		{Name: "SHARED_VAR", Value: "sv", Source: "shared"},
		{Name: "USER_VAR", Value: "uv", Source: "user"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	var sec corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "app-env"}, &sec); err != nil {
		t.Fatal(err)
	}

	bindingCSV := sec.Annotations[AnnotationBindingKeys]
	if !containsKey(bindingCSV, "DB_HOST") {
		t.Errorf("DB_HOST not in binding annotation: %q", bindingCSV)
	}
	generatedCSV := sec.Annotations[AnnotationGeneratedKeys]
	if !containsKey(generatedCSV, "JWT_SECRET") {
		t.Errorf("JWT_SECRET not in generated annotation: %q", generatedCSV)
	}
	sharedCSV := sec.Annotations[AnnotationSharedKeys]
	if !containsKey(sharedCSV, "SHARED_VAR") {
		t.Errorf("SHARED_VAR not in shared annotation: %q", sharedCSV)
	}
	// USER_VAR should not appear in any source annotation.
	for _, ann := range []string{bindingCSV, generatedCSV, sharedCSV} {
		if containsKey(ann, "USER_VAR") {
			t.Errorf("USER_VAR should not be in any source annotation, found in %q", ann)
		}
	}
}

func TestReplaceSourceUpdatesAnnotations(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}
	ctx := context.Background()
	ns := "pj-test-production"

	// Seed with two binding vars.
	if err := store.Set(ctx, ns, "app", []Env{
		{Name: "OLD_HOST", Value: "old", Source: "binding"},
		{Name: "OLD_PORT", Value: "5432", Source: "binding"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	// Replace binding source with one new var.
	if err := store.ReplaceSource(ctx, ns, "app", "binding", []Env{
		{Name: "NEW_HOST", Value: "new", Source: "binding"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	var sec corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "app-env"}, &sec); err != nil {
		t.Fatal(err)
	}
	csv := sec.Annotations[AnnotationBindingKeys]
	if containsKey(csv, "OLD_HOST") || containsKey(csv, "OLD_PORT") {
		t.Errorf("old binding keys should be removed from annotation: %q", csv)
	}
	if !containsKey(csv, "NEW_HOST") {
		t.Errorf("NEW_HOST should be in binding annotation: %q", csv)
	}
}

func TestMergeSharedPreservesExisting(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &Store{Client: c}
	ctx := context.Background()
	ns := "pj-test-production"

	if err := store.SetShared(ctx, ns, []Env{
		{Name: "LOG_LEVEL", Value: "info", Source: "shared"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	if err := store.MergeShared(ctx, ns, []Env{
		{Name: "SENTRY_DSN", Value: "https://sentry/1", Source: "shared"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetShared(ctx, ns)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]Env)
	for _, e := range got {
		byName[e.Name] = e
	}
	if byName["LOG_LEVEL"].Value != "info" {
		t.Errorf("LOG_LEVEL wiped by MergeShared, got %q", byName["LOG_LEVEL"].Value)
	}
	if byName["SENTRY_DSN"].Value != "https://sentry/1" {
		t.Errorf("SENTRY_DSN = %q, want https://sentry/1", byName["SENTRY_DSN"].Value)
	}
}

// splitCSV splits a comma-separated string, trimming spaces, filtering empty.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// containsKey reports whether a CSV annotation string contains the given key.
func containsKey(csv, key string) bool {
	for _, k := range splitCSV(csv) {
		if k == key {
			return true
		}
	}
	return false
}
