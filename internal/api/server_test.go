package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/api"
	"github.com/MC-Meesh/mortise/internal/auth"
)

// newTestServer builds an API server wired against the given k8s client with
// a real admin user + JWT. Returns the server and the bearer token.
func newTestServer(t *testing.T, k8sClient client.Client) (*api.Server, string) {
	return newTestServerAs(t, k8sClient, auth.RoleAdmin)
}

// newTestServerAs is like newTestServer but lets tests pick the principal's
// role (admin vs member) so they can exercise authorization boundaries.
func newTestServerAs(t *testing.T, k8sClient client.Client, role auth.Role) (*api.Server, string) {
	t.Helper()
	ctx := context.Background()

	_ = k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"},
	})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)

	email := "test@example.com"
	if role == auth.RoleMember {
		email = "member@example.com"
	}
	if err := authProvider.CreateUser(ctx, email, "testpass", role); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	principal, err := authProvider.Authenticate(ctx, auth.Credentials{Email: email, Password: "testpass"})
	if err != nil {
		t.Fatalf("authenticate test user: %v", err)
	}
	token, err := jwtHelper.GenerateToken(ctx, principal)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	srv := api.NewServer(k8sClient, fake.NewClientset(), authProvider, jwtHelper, nil)
	testToken = token
	return srv, token
}

// newAdminServer is a convenience wrapper for tests that just need an admin
// client without caring about the token.
func newAdminServer(t *testing.T, k8sClient client.Client) *api.Server {
	t.Helper()
	srv, _ := newTestServer(t, k8sClient)
	return srv
}

func setupEnvtest(t *testing.T) client.Client {
	t.Helper()

	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")},
	}

	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = testEnv.Stop() })

	err = mortisev1alpha1.AddToScheme(scheme.Scheme)
	if err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	// "default" namespace exists by default in k8s — no need to create it.
	return k8sClient
}

// seedProject creates a Project CRD and its backing namespace so tests can
// exercise handlers that require both to be present. Returns the namespace
// name the handlers will resolve to.
func seedProject(t *testing.T, c client.Client, name string) string {
	t.Helper()
	ctx := context.Background()

	proj := &mortisev1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := c.Create(ctx, proj); err != nil {
		t.Fatalf("create project %q: %v", name, err)
	}

	nsName := "project-" + name
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	if err := c.Create(ctx, ns); err != nil {
		t.Fatalf("create namespace %q: %v", nsName, err)
	}

	// Reflect the Project->Namespace binding in status so handlers can read it.
	proj.Status.Phase = mortisev1alpha1.ProjectPhaseReady
	proj.Status.Namespace = nsName
	if err := c.Status().Update(ctx, proj); err != nil {
		t.Fatalf("update project status: %v", err)
	}

	return nsName
}

// testToken is set by the first call to newTestServer. Tests that use
// doRequest (no explicit token) pick it up here.
var testToken string

func doRequest(handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	return doRequestWithToken(handler, method, path, body, testToken)
}

func doRequestWithToken(handler http.Handler, method, path string, body any, token string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestCreateAndGetApp(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	createBody := map[string]any{
		"name": "myapp",
		"spec": map[string]any{
			"source": map[string]any{
				"type":  "image",
				"image": "nginx:1.25.0",
			},
		},
	}

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps", createBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("create app: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	w = doRequest(h, http.MethodGet, "/api/projects/default/apps/myapp", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get app: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var app mortisev1alpha1.App
	_ = json.NewDecoder(w.Body).Decode(&app)
	if app.Spec.Source.Image != "nginx:1.25.0" {
		t.Errorf("expected image nginx:1.25.0, got %s", app.Spec.Source.Image)
	}
	if app.Namespace != "project-default" {
		t.Errorf("expected namespace project-default, got %s", app.Namespace)
	}
}

func TestListAppsInProject(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	for _, name := range []string{"app-a", "app-b"} {
		doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
			"name": name,
			"spec": map[string]any{
				"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
			},
		})
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list apps: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var apps []mortisev1alpha1.App
	_ = json.NewDecoder(w.Body).Decode(&apps)
	if len(apps) != 2 {
		t.Errorf("expected 2 apps, got %d", len(apps))
	}
}

func TestUpdateApp(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "update-me",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	w := doRequest(h, http.MethodPut, "/api/projects/default/apps/update-me", map[string]any{
		"source": map[string]any{"type": "image", "image": "nginx:1.26.0"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update app: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var app mortisev1alpha1.App
	_ = json.NewDecoder(w.Body).Decode(&app)
	if app.Spec.Source.Image != "nginx:1.26.0" {
		t.Errorf("expected image nginx:1.26.0, got %s", app.Spec.Source.Image)
	}
}

func TestDeleteApp(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "delete-me",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	w := doRequest(h, http.MethodDelete, "/api/projects/default/apps/delete-me", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delete app: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = doRequest(h, http.MethodGet, "/api/projects/default/apps/delete-me", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", w.Code)
	}
}

func TestDeploy(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "deploy-target",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/deploy-target/deploy", map[string]any{
		"image": "nginx:1.26.0",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("deploy: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify image was updated on the CRD.
	var app mortisev1alpha1.App
	err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "deploy-target", Namespace: "project-default"}, &app)
	if err != nil {
		t.Fatalf("get app after deploy: %v", err)
	}
	if app.Spec.Source.Image != "nginx:1.26.0" {
		t.Errorf("expected image nginx:1.26.0, got %s", app.Spec.Source.Image)
	}
}

func TestSecretsCRUD(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/myapp/secrets", map[string]any{
		"name": "db-creds",
		"data": map[string]string{"password": "s3cret"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create secret: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	w = doRequest(h, http.MethodGet, "/api/projects/default/apps/myapp/secrets", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list secrets: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var secrets []map[string]any
	_ = json.NewDecoder(w.Body).Decode(&secrets)
	if len(secrets) != 1 {
		t.Fatalf("expected 1 secret, got %d", len(secrets))
	}
	if secrets[0]["name"] != "db-creds" {
		t.Errorf("expected secret name db-creds, got %v", secrets[0]["name"])
	}

	w = doRequest(h, http.MethodDelete, "/api/projects/default/apps/myapp/secrets/db-creds", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delete secret: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = doRequest(h, http.MethodGet, "/api/projects/default/apps/myapp/secrets", nil)
	_ = json.NewDecoder(w.Body).Decode(&secrets)
	if len(secrets) != 0 {
		t.Errorf("expected 0 secrets after delete, got %d", len(secrets))
	}
}

func TestUnauthenticatedRequest(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestGetAppNotFound(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/nonexistent", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestGetAppInNonexistentProjectIs404 verifies that accessing an app inside a
// project that doesn't exist returns 404 at the project-resolution step, not
// 500 from a missing namespace lookup.
func TestGetAppInNonexistentProjectIs404(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/projects/ghost/apps/anything", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for app in nonexistent project, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] == "" {
		t.Errorf("expected error body in 404 response")
	}
}

func TestRollback(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	// Create an app with deploy history in status.
	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "rollback-app", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.27"},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	// Create the Deployment the handler will patch.
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "rollback-app-production", Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "rollback-app"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "rollback-app"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx:1.27"}}},
			},
		},
	}
	if err := k8sClient.Create(ctx, dep); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// Seed deploy history on app status.
	app.Status.Environments = []mortisev1alpha1.EnvironmentStatus{
		{
			Name:         "production",
			CurrentImage: "nginx:1.27",
			DeployHistory: []mortisev1alpha1.DeployRecord{
				{Image: "nginx:1.26", Timestamp: metav1.Now()},
				{Image: "nginx:1.27", Timestamp: metav1.Now()},
			},
		},
	}
	if err := k8sClient.Status().Update(ctx, app); err != nil {
		t.Fatalf("update app status: %v", err)
	}

	// Rollback to index 0 (nginx:1.26).
	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/rollback-app/rollback", map[string]any{
		"environment": "production",
		"index":       0,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("rollback: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var record mortisev1alpha1.DeployRecord
	_ = json.NewDecoder(w.Body).Decode(&record)
	if record.Image != "nginx:1.26" {
		t.Errorf("expected rollback to nginx:1.26, got %s", record.Image)
	}

	// Verify Deployment was patched.
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "rollback-app-production", Namespace: ns}, dep); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if dep.Spec.Template.Spec.Containers[0].Image != "nginx:1.26" {
		t.Errorf("expected deployment image nginx:1.26, got %s", dep.Spec.Template.Spec.Containers[0].Image)
	}
}

func TestRollbackInvalidIndex(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "rollback-bad", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.27"},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app.Status.Environments = []mortisev1alpha1.EnvironmentStatus{
		{Name: "production", DeployHistory: []mortisev1alpha1.DeployRecord{
			{Image: "nginx:1.26", Timestamp: metav1.Now()},
		}},
	}
	if err := k8sClient.Status().Update(ctx, app); err != nil {
		t.Fatalf("update status: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/rollback-bad/rollback", map[string]any{
		"environment": "production",
		"index":       5,
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid index, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRollbackInvalidEnv(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "rollback-noenv", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.27"},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/rollback-noenv/rollback", map[string]any{
		"environment": "staging",
		"index":       0,
	})
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent env, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRollbackRequiresAuth(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodPost, "/api/projects/default/apps/x/rollback", map[string]any{
		"environment": "production",
		"index":       0,
	}, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestPromote(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "promote-app", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.27"},
			Environments: []mortisev1alpha1.Environment{
				{Name: "staging"},
				{Name: "production"},
			},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	// Create Deployments for both envs.
	for _, envName := range []string{"staging", "production"} {
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "promote-app-" + envName, Namespace: ns},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "promote-app", "env": envName}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "promote-app", "env": envName}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25"}}},
				},
			},
		}
		if err := k8sClient.Create(ctx, dep); err != nil {
			t.Fatalf("create deployment %s: %v", envName, err)
		}
	}

	// Seed staging with a current image.
	app.Status.Environments = []mortisev1alpha1.EnvironmentStatus{
		{Name: "staging", CurrentImage: "nginx:1.27", CurrentDigest: "sha256:abc123"},
		{Name: "production", CurrentImage: "nginx:1.25"},
	}
	if err := k8sClient.Status().Update(ctx, app); err != nil {
		t.Fatalf("update status: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/promote-app/promote", map[string]any{
		"from": "staging",
		"to":   "production",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("promote: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify production Deployment has the staging image.
	var dep appsv1.Deployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "promote-app-production", Namespace: ns}, &dep); err != nil {
		t.Fatalf("get production deployment: %v", err)
	}
	if dep.Spec.Template.Spec.Containers[0].Image != "sha256:abc123" {
		t.Errorf("expected production image sha256:abc123, got %s", dep.Spec.Template.Spec.Containers[0].Image)
	}
}

func TestPromoteInvalidEnv(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "promote-bad", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.27"},
			Environments: []mortisev1alpha1.Environment{
				{Name: "staging"},
			},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	// No status at all — source env not found.
	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/promote-bad/promote", map[string]any{
		"from": "staging",
		"to":   "production",
	})
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing source env status, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPromoteSameEnv(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/any-app/promote", map[string]any{
		"from": "staging",
		"to":   "staging",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for same env, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPromoteRequiresAuth(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodPost, "/api/projects/default/apps/x/promote", map[string]any{
		"from": "staging",
		"to":   "production",
	}, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}
