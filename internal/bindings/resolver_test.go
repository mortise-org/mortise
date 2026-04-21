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

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/bindings"
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
