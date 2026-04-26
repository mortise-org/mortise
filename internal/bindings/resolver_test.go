package bindings_test

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/bindings"
)

func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortisev1alpha1 to scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

// newDB returns an App that declares credentials, so the resolver has work to do.
// The App CRD lives in the control namespace `pj-{project}` per the per-env-ns model.
func newDB(name, controlNs string) *mortisev1alpha1.App {
	return &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: controlNs},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
			Credentials: []mortisev1alpha1.Credential{
				{Name: "host"},
				{Name: "port"},
				{Name: "username", Value: "postgres"},
				{Name: "password", Value: "hunter2"},
			},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
}

func credentialsSecret(appName, namespace string, data map[string]string) *corev1.Secret {
	d := make(map[string][]byte, len(data))
	for k, v := range data {
		d[k] = []byte(v)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: appName + "-credentials", Namespace: namespace},
		Data:       d,
	}
}

func TestResolveSameProjectBinding(t *testing.T) {
	db := newDB("db", "pj-web")
	creds := credentialsSecret("db", "pj-web-production", map[string]string{
		"username": "postgres",
		"password": "hunter2",
	})
	c := newFakeClient(t, db, creds)
	r := &bindings.Resolver{Client: c}

	vars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "db"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	hostVar := findVar(vars, "DB_HOST")
	if hostVar == nil {
		t.Fatal("expected host env var to be set")
	}
	if hostVar.Value != "db.pj-web-production.svc.cluster.local" {
		t.Errorf("expected host pointing at same-project env service, got %q", hostVar.Value)
	}

	pwVar := findVar(vars, "DB_PASSWORD")
	if pwVar == nil {
		t.Fatal("expected DB_PASSWORD")
	}
	if pwVar.Value != "hunter2" {
		t.Errorf("DB_PASSWORD: got %q, want %q", pwVar.Value, "hunter2")
	}
}

func TestMultipleBindingsInSameProject(t *testing.T) {
	db := newDB("db", "pj-web")
	dbCreds := credentialsSecret("db", "pj-web-production", map[string]string{
		"username": "postgres",
		"password": "hunter2",
	})
	cache := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "redis:7"},
			Network: mortisev1alpha1.NetworkConfig{Port: 6379},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	c := newFakeClient(t, db, dbCreds, cache)
	r := &bindings.Resolver{Client: c}

	vars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "db"},
		{Ref: "cache"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if findVar(vars, "DB_HOST") == nil {
		t.Error("expected DB_HOST")
	}
	if findVar(vars, "CACHE_HOST") == nil {
		t.Error("expected CACHE_HOST")
	}
}

func TestResolveMissingBindingReturnsError(t *testing.T) {
	c := newFakeClient(t)
	r := &bindings.Resolver{Client: c}

	_, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "does-not-exist"},
	})
	if err == nil {
		t.Fatal("expected error for missing bound app, got nil")
	}
}

func TestResolveExternalSourceBinding(t *testing.T) {
	redis := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "redis", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type: mortisev1alpha1.SourceTypeExternal,
				External: &mortisev1alpha1.ExternalSource{
					Host: "redis.example.com",
					Port: 6379,
				},
			},
			Credentials: []mortisev1alpha1.Credential{
				{Name: "host"},
				{Name: "port"},
			},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	c := newFakeClient(t, redis)
	r := &bindings.Resolver{Client: c}

	vars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "redis"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	hostVar := findVar(vars, "REDIS_HOST")
	if hostVar == nil {
		t.Fatal("expected REDIS_HOST env var to be set")
	}
	if hostVar.Value != "redis.example.com" {
		t.Errorf("expected external host, got %q", hostVar.Value)
	}

	portVar := findVar(vars, "REDIS_PORT")
	if portVar == nil {
		t.Fatal("expected REDIS_PORT env var to be set")
	}
	if portVar.Value != "6379" {
		t.Errorf("expected port %q, got %q", "6379", portVar.Value)
	}
}

func TestResolveExternalSourceNoPort(t *testing.T) {
	redis := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "redis", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type: mortisev1alpha1.SourceTypeExternal,
				External: &mortisev1alpha1.ExternalSource{
					Host: "redis.example.com",
					Port: 0,
				},
			},
			Credentials: []mortisev1alpha1.Credential{
				{Name: "host"},
				{Name: "port"},
			},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	c := newFakeClient(t, redis)
	r := &bindings.Resolver{Client: c}

	vars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "redis"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	hostVar := findVar(vars, "REDIS_HOST")
	if hostVar == nil {
		t.Fatal("expected REDIS_HOST env var to be set")
	}
	if hostVar.Value != "redis.example.com" {
		t.Errorf("expected external host, got %q", hostVar.Value)
	}

	portVar := findVar(vars, "REDIS_PORT")
	if portVar == nil {
		t.Fatal("expected REDIS_PORT env var to be set")
	}
	if portVar.Value != "" {
		t.Errorf("expected empty port for zero port value, got %q", portVar.Value)
	}
}

func TestResolveAppWithNoCredentials(t *testing.T) {
	svc := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "sidecar", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25"},
			Network: mortisev1alpha1.NetworkConfig{Port: 80},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	c := newFakeClient(t, svc)
	r := &bindings.Resolver{Client: c}

	vars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "sidecar"},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if findVar(vars, "SIDECAR_HOST") == nil {
		t.Error("expected SIDECAR_HOST")
	}
	if findVar(vars, "SIDECAR_PORT") == nil {
		t.Error("expected SIDECAR_PORT")
	}
	if findVar(vars, "SIDECAR_URL") != nil {
		t.Error("nginx should not generate a URL")
	}
}

func TestResolveMultipleBindingsNoPrefixCollision(t *testing.T) {
	pg := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "postgres", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
			Network: mortisev1alpha1.NetworkConfig{Port: 5432},
			Credentials: []mortisev1alpha1.Credential{
				{Name: "host"}, {Name: "port"}, {Name: "password", Value: "pgpass"},
			},
			Environments: []mortisev1alpha1.Environment{{Name: "production"}},
		},
	}
	pgCreds := credentialsSecret("postgres", "pj-web-production", map[string]string{
		"password": "pgpass",
	})
	redis := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source:       mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "redis:7"},
			Network:      mortisev1alpha1.NetworkConfig{Port: 6379},
			Environments: []mortisev1alpha1.Environment{{Name: "production"}},
		},
	}
	c := newFakeClient(t, pg, pgCreds, redis)
	r := &bindings.Resolver{Client: c}

	vars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "postgres"},
		{Ref: "cache"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if findVar(vars, "POSTGRES_HOST") == nil {
		t.Error("expected POSTGRES_HOST")
	}
	if findVar(vars, "POSTGRES_PORT") == nil {
		t.Error("expected POSTGRES_PORT")
	}
	if findVar(vars, "POSTGRES_URL") == nil {
		t.Error("expected POSTGRES_URL for postgres image")
	}
	if findVar(vars, "POSTGRES_PASSWORD") == nil {
		t.Error("expected POSTGRES_PASSWORD from credentials")
	}
	if findVar(vars, "CACHE_HOST") == nil {
		t.Error("expected CACHE_HOST")
	}
	if findVar(vars, "CACHE_PORT") == nil {
		t.Error("expected CACHE_PORT")
	}
	if findVar(vars, "CACHE_URL") == nil {
		t.Error("expected CACHE_URL for redis image")
	}

	if findVar(vars, "host") != nil {
		t.Error("unprefixed 'host' should not exist")
	}
	if findVar(vars, "port") != nil {
		t.Error("unprefixed 'port' should not exist")
	}
}

func TestResolveBoundAppDisabledInEnv(t *testing.T) {
	disabled := false
	svc := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
			Credentials: []mortisev1alpha1.Credential{
				{Name: "host"},
			},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production", Enabled: &disabled},
			},
		},
	}
	c := newFakeClient(t, svc)
	r := &bindings.Resolver{Client: c}

	_, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "db"},
	})
	if err == nil {
		t.Fatal("expected error when bound app is disabled in env, got nil")
	}
	if !strings.Contains(err.Error(), "enabled instance") {
		t.Errorf("expected error to mention enabled status, got: %v", err)
	}
}

func TestResolveCredentialsMissingSecretReturnsError(t *testing.T) {
	db := newDB("db", "pj-web")
	c := newFakeClient(t, db)
	r := &bindings.Resolver{Client: c}

	_, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "db"},
	})
	if err == nil {
		t.Fatal("expected error when credentials Secret is missing")
	}
	if !strings.Contains(err.Error(), "credentials") {
		t.Errorf("expected error to mention credentials, got: %v", err)
	}
}

func TestToEnvPrefixSanitizesDots(t *testing.T) {
	vars, err := resolveWithApp(t, "my.database", "pj-web", "postgres:16")
	if err != nil {
		t.Fatal(err)
	}
	host := findVar(vars, "MY_DATABASE_HOST")
	if host == nil {
		t.Fatal("expected MY_DATABASE_HOST after dot sanitization")
	}
}

func TestToEnvPrefixStripsLeadingDigits(t *testing.T) {
	vars, err := resolveWithApp(t, "3scale", "pj-web", "nginx:1.25")
	if err != nil {
		t.Fatal(err)
	}
	host := findVar(vars, "SCALE_HOST")
	if host == nil {
		t.Fatal("expected SCALE_HOST after leading digit strip")
	}
}

func TestAutoURLRegistryPrefixedImage(t *testing.T) {
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "docker.io/library/postgres:16"},
			Network: mortisev1alpha1.NetworkConfig{Port: 5432},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	c := newFakeClient(t, app)
	r := &bindings.Resolver{Client: c}

	vars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "db"},
	})
	if err != nil {
		t.Fatal(err)
	}
	urlVar := findVar(vars, "DB_URL")
	if urlVar == nil {
		t.Fatal("expected DB_URL for registry-prefixed postgres image")
	}
	if !strings.HasPrefix(urlVar.Value, "postgres://") {
		t.Errorf("expected postgres:// URL, got %q", urlVar.Value)
	}
}

func resolveWithApp(t *testing.T, appName, controlNs, image string) ([]bindings.ResolvedVar, error) {
	t.Helper()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: controlNs},
		Spec: mortisev1alpha1.AppSpec{
			Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: image},
			Network: mortisev1alpha1.NetworkConfig{Port: 8080},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	c := newFakeClient(t, app)
	r := &bindings.Resolver{Client: c}
	return r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: appName},
	})
}

func findVar(vars []bindings.ResolvedVar, name string) *bindings.ResolvedVar {
	for i := range vars {
		if vars[i].Name == name {
			return &vars[i]
		}
	}
	return nil
}

func TestInternalPortZeroDefaultsTo8080(t *testing.T) {
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25"},
			Network: mortisev1alpha1.NetworkConfig{Port: 0}, // zero — should default to 8080
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	c := newFakeClient(t, app)
	r := &bindings.Resolver{Client: c}

	vars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "svc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	portVar := findVar(vars, "SVC_PORT")
	if portVar == nil {
		t.Fatal("expected SVC_PORT")
	}
	if portVar.Value != "8080" {
		t.Errorf("expected SVC_PORT=8080 for zero port, got %q", portVar.Value)
	}
}

func TestEmptyBindingsReturnsEmpty(t *testing.T) {
	c := newFakeClient(t)
	r := &bindings.Resolver{Client: c}

	vars, err := r.Resolve(context.Background(), "web", "production", nil)
	if err != nil {
		t.Fatalf("empty bindings should not return error, got %v", err)
	}
	if len(vars) != 0 {
		t.Errorf("expected empty result for empty bindings, got %v", vars)
	}
}

func TestBoundAppNotInEnvListIsEnabled(t *testing.T) {
	// App has only "staging" in its env list; resolving for "production"
	// should succeed because the default is enabled when env not listed.
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25"},
			Network: mortisev1alpha1.NetworkConfig{Port: 80},
			Environments: []mortisev1alpha1.Environment{
				{Name: "staging"},
			},
		},
	}
	c := newFakeClient(t, app)
	r := &bindings.Resolver{Client: c}

	vars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "svc"},
	})
	if err != nil {
		t.Fatalf("expected no error when env not in list (default enabled), got %v", err)
	}
	if findVar(vars, "SVC_HOST") == nil {
		t.Error("expected SVC_HOST in result")
	}
}

func TestBoundAppNilEnabledInEnvIsEnabled(t *testing.T) {
	// App lists "production" with Enabled=nil — should be treated as enabled.
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25"},
			Network: mortisev1alpha1.NetworkConfig{Port: 80},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production", Enabled: nil},
			},
		},
	}
	c := newFakeClient(t, app)
	r := &bindings.Resolver{Client: c}

	vars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "svc"},
	})
	if err != nil {
		t.Fatalf("expected no error for nil Enabled, got %v", err)
	}
	if findVar(vars, "SVC_HOST") == nil {
		t.Error("expected SVC_HOST")
	}
}

func TestCredentialKeyMissingInSecretReturnsEmpty(t *testing.T) {
	db := newDB("db", "pj-web")
	// Credentials Secret exists but is missing the "password" key.
	creds := credentialsSecret("db", "pj-web-production", map[string]string{
		"username": "postgres",
		// "password" intentionally absent
	})
	c := newFakeClient(t, db, creds)
	r := &bindings.Resolver{Client: c}

	vars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "db"},
	})
	if err != nil {
		t.Fatalf("missing credential key should not error, got %v", err)
	}
	pwVar := findVar(vars, "DB_PASSWORD")
	if pwVar == nil {
		t.Fatal("expected DB_PASSWORD in result even when key missing")
	}
	if pwVar.Value != "" {
		t.Errorf("expected empty string for missing key, got %q", pwVar.Value)
	}
}

func TestResolveInternalAndExternalCombined(t *testing.T) {
	internal := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "redis:7"},
			Network: mortisev1alpha1.NetworkConfig{Port: 6379},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	external := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "mailer", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type: mortisev1alpha1.SourceTypeExternal,
				External: &mortisev1alpha1.ExternalSource{
					Host: "smtp.example.com",
					Port: 587,
				},
			},
			Environments: []mortisev1alpha1.Environment{{Name: "production"}},
		},
	}
	c := newFakeClient(t, internal, external)
	r := &bindings.Resolver{Client: c}

	vars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "cache"},
		{Ref: "mailer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if findVar(vars, "CACHE_HOST") == nil {
		t.Error("expected CACHE_HOST (internal binding)")
	}
	if findVar(vars, "MAILER_HOST") == nil {
		t.Error("expected MAILER_HOST (external binding)")
	}
	mailerHost := findVar(vars, "MAILER_HOST")
	if mailerHost.Value != "smtp.example.com" {
		t.Errorf("MAILER_HOST = %q, want smtp.example.com", mailerHost.Value)
	}
	mailerPort := findVar(vars, "MAILER_PORT")
	if mailerPort == nil || mailerPort.Value != "587" {
		t.Errorf("MAILER_PORT = %v, want 587", mailerPort)
	}
}

func TestPrefixAllDigitsFallsBackToBinding(t *testing.T) {
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "123", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25"},
			Network: mortisev1alpha1.NetworkConfig{Port: 80},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	c := newFakeClient(t, app)
	r := &bindings.Resolver{Client: c}

	vars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if findVar(vars, "BINDING_HOST") == nil {
		t.Error("expected BINDING_HOST when all-digit app name strips to empty")
	}
	if findVar(vars, "BINDING_PORT") == nil {
		t.Error("expected BINDING_PORT when all-digit app name strips to empty")
	}
}

func TestAutoURLForRedisImage(t *testing.T) {
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "redis:7"},
			Network: mortisev1alpha1.NetworkConfig{Port: 6379},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	c := newFakeClient(t, app)
	r := &bindings.Resolver{Client: c}

	vars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "cache"},
	})
	if err != nil {
		t.Fatal(err)
	}
	urlVar := findVar(vars, "CACHE_URL")
	if urlVar == nil {
		t.Fatal("expected CACHE_URL for redis image")
	}
	if !strings.HasPrefix(urlVar.Value, "redis://") {
		t.Errorf("expected redis:// URL, got %q", urlVar.Value)
	}
}

func TestAutoURLForMySQLImage(t *testing.T) {
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "mysql:8"},
			Network: mortisev1alpha1.NetworkConfig{Port: 3306},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	c := newFakeClient(t, app)
	r := &bindings.Resolver{Client: c}

	vars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "db"},
	})
	if err != nil {
		t.Fatal(err)
	}
	urlVar := findVar(vars, "DB_URL")
	if urlVar == nil {
		t.Fatal("expected DB_URL for mysql image")
	}
	if !strings.HasPrefix(urlVar.Value, "mysql://") {
		t.Errorf("expected mysql:// URL, got %q", urlVar.Value)
	}
}

func TestAutoURLForMongoImage(t *testing.T) {
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "docstore", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "mongo:6"},
			Network: mortisev1alpha1.NetworkConfig{Port: 27017},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	c := newFakeClient(t, app)
	r := &bindings.Resolver{Client: c}

	vars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "docstore"},
	})
	if err != nil {
		t.Fatal(err)
	}
	urlVar := findVar(vars, "DOCSTORE_URL")
	if urlVar == nil {
		t.Fatal("expected DOCSTORE_URL for mongo image")
	}
	if !strings.HasPrefix(urlVar.Value, "mongodb://") {
		t.Errorf("expected mongodb:// URL, got %q", urlVar.Value)
	}
}
