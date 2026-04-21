package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/api"
	"github.com/MC-Meesh/mortise/internal/auth"
	"github.com/MC-Meesh/mortise/internal/authz"
	"github.com/MC-Meesh/mortise/internal/constants"
)

// sharedCfg is the envtest rest.Config. Started once by TestMain and reused
// by every test via setupEnvtest. Starting envtest takes ~4s, so doing it
// once per package (instead of per test) is the main cost win.
var sharedCfg *rest.Config

func TestMain(m *testing.M) {
	if err := mortisev1alpha1.AddToScheme(scheme.Scheme); err != nil {
		fmt.Fprintln(os.Stderr, "add scheme:", err)
		os.Exit(1)
	}

	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")},
	}
	cfg, err := testEnv.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "start envtest:", err)
		os.Exit(1)
	}
	sharedCfg = cfg

	// mortise-system holds user Secrets, webhook Secrets, and platform
	// config. Create it once so the per-test tolerate-AlreadyExists in
	// newTestServerAs keeps working.
	if c, err := client.New(sharedCfg, client.Options{Scheme: scheme.Scheme}); err == nil {
		_ = c.Create(context.Background(), &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"},
		})
	}

	code := m.Run()
	_ = testEnv.Stop()
	os.Exit(code)
}

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

	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine())
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

// setupEnvtest returns a client bound to the package-shared envtest cluster.
// Register a per-test cleanup that deletes every tenant resource so the next
// test sees a clean state even though the apiserver + etcd persist. Envtest
// can't finalize Namespaces (no GC controller runs) so those leak across
// tests; seedProject tolerates AlreadyExists to deal with that.
func setupEnvtest(t *testing.T) client.Client {
	t.Helper()

	k8sClient, err := client.New(sharedCfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	t.Cleanup(func() { cleanupTenantResources(t, k8sClient) })
	return k8sClient
}

// cleanupTenantResources wipes everything a test may have created: Apps,
// PreviewEnvironments, Projects, GitProviders, and Secrets in mortise-system
// (user Secrets, webhook Secrets, invite Secrets). Finalizers are cleared
// first because no controllers run in envtest to remove them, so a plain
// Delete would leave objects stuck in Terminating and collide with the next
// test's Create.
func cleanupTenantResources(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()

	var apps mortisev1alpha1.AppList
	if err := c.List(ctx, &apps); err == nil {
		for i := range apps.Items {
			app := &apps.Items[i]
			if len(app.Finalizers) > 0 {
				app.Finalizers = nil
				_ = c.Update(ctx, app)
			}
			_ = c.Delete(ctx, app)
		}
	}

	var pes mortisev1alpha1.PreviewEnvironmentList
	if err := c.List(ctx, &pes); err == nil {
		for i := range pes.Items {
			pe := &pes.Items[i]
			if len(pe.Finalizers) > 0 {
				pe.Finalizers = nil
				_ = c.Update(ctx, pe)
			}
			_ = c.Delete(ctx, pe)
		}
	}

	var projs mortisev1alpha1.ProjectList
	if err := c.List(ctx, &projs); err == nil {
		for i := range projs.Items {
			proj := &projs.Items[i]
			if len(proj.Finalizers) > 0 {
				proj.Finalizers = nil
				_ = c.Update(ctx, proj)
			}
			_ = c.Delete(ctx, proj)
		}
	}

	var gps mortisev1alpha1.GitProviderList
	if err := c.List(ctx, &gps); err == nil {
		for i := range gps.Items {
			gp := &gps.Items[i]
			if len(gp.Finalizers) > 0 {
				gp.Finalizers = nil
				_ = c.Update(ctx, gp)
			}
			_ = c.Delete(ctx, gp)
		}
	}

	var pcs mortisev1alpha1.PlatformConfigList
	if err := c.List(ctx, &pcs); err == nil {
		for i := range pcs.Items {
			pc := &pcs.Items[i]
			if len(pc.Finalizers) > 0 {
				pc.Finalizers = nil
				_ = c.Update(ctx, pc)
			}
			_ = c.Delete(ctx, pc)
		}
	}

	var teams mortisev1alpha1.TeamList
	if err := c.List(ctx, &teams); err == nil {
		for i := range teams.Items {
			team := &teams.Items[i]
			if len(team.Finalizers) > 0 {
				team.Finalizers = nil
				_ = c.Update(ctx, team)
			}
			_ = c.Delete(ctx, team)
		}
	}

	var secrets corev1.SecretList
	if err := c.List(ctx, &secrets, client.InNamespace("mortise-system")); err == nil {
		for i := range secrets.Items {
			_ = c.Delete(ctx, &secrets.Items[i])
		}
	}
}

// seedProject creates a Project CRD and its backing namespace so tests can
// exercise handlers that require both to be present. Returns the namespace
// name the handlers will resolve to.
//
// The project is seeded with a single `production` environment so env-scoped
// handlers (env vars, domains, deploys) work out of the box — the real
// project controller seeds the same default, this just short-circuits it for
// envtest where no reconcile loop is running.
func seedProject(t *testing.T, c client.Client, name string, envs ...string) string {
	t.Helper()
	ctx := context.Background()

	if len(envs) == 0 {
		envs = []string{"production"}
	}
	specEnvs := make([]mortisev1alpha1.ProjectEnvironment, 0, len(envs))
	for i, env := range envs {
		specEnvs = append(specEnvs, mortisev1alpha1.ProjectEnvironment{Name: env, DisplayOrder: i})
	}

	proj := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       mortisev1alpha1.ProjectSpec{Environments: specEnvs},
	}
	if err := c.Create(ctx, proj); err != nil {
		t.Fatalf("create project %q: %v", name, err)
	}

	nsName := constants.ControlNamespace(name)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	if err := c.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace %q: %v", nsName, err)
	}

	// Per-env workload namespaces (pj-{project}-{env}) are where Pods,
	// Deployments, Services, and other env-scoped resources live. Pivot E
	// API handlers query these namespaces directly, so seed them alongside
	// the control ns. Namespaces leak across tests in envtest (no GC
	// controller runs to finalize them), so tolerate AlreadyExists.
	for _, env := range envs {
		envNs := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: constants.EnvNamespace(name, env)}}
		if err := c.Create(ctx, envNs); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("create env namespace %q: %v", envNs.Name, err)
		}
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

// doRequestRawBody sends a request with a pre-formed body string (not
// JSON-encoded). Use this for testing invalid-JSON and .env import paths.
func doRequestRawBody(handler http.Handler, method, path, rawBody, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(rawBody))
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
	if app.Namespace != "pj-default" {
		t.Errorf("expected namespace pj-default, got %s", app.Namespace)
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
	err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "deploy-target", Namespace: "pj-default"}, &app)
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

	// Create the Deployment the handler will patch. Workload resources live
	// in the per-env namespace.
	envNs := constants.EnvNamespace("default", "production")
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "rollback-app-production", Namespace: envNs},
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
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "rollback-app-production", Namespace: envNs}, dep); err != nil {
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
	ns := seedProject(t, k8sClient, "default", "staging", "production")

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

	// Create Deployments for both envs in their per-env workload namespaces.
	for _, envName := range []string{"staging", "production"} {
		depNs := constants.EnvNamespace("default", envName)
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "promote-app-" + envName, Namespace: depNs},
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
	prodNs := constants.EnvNamespace("default", "production")
	var dep appsv1.Deployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "promote-app-production", Namespace: prodNs}, &dep); err != nil {
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

// TestMemberCanCRUDApps verifies members have full CRUD access to apps.
func TestMemberCanCRUDApps(t *testing.T) {
	k8sClient := setupEnvtest(t)
	seedProject(t, k8sClient, "default")
	srv, _ := newTestServerAs(t, k8sClient, auth.RoleMember)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "member-app",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	w = doRequest(h, http.MethodGet, "/api/projects/default/apps/member-app", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = doRequest(h, http.MethodGet, "/api/projects/default/apps", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = doRequest(h, http.MethodPut, "/api/projects/default/apps/member-app", map[string]any{
		"source": map[string]any{"type": "image", "image": "nginx:1.26.0"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = doRequest(h, http.MethodDelete, "/api/projects/default/apps/member-app", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// TestMemberCanCRUDSecrets verifies members have full CRUD access to secrets.
func TestMemberCanCRUDSecrets(t *testing.T) {
	k8sClient := setupEnvtest(t)
	seedProject(t, k8sClient, "default")
	srv, _ := newTestServerAs(t, k8sClient, auth.RoleMember)
	h := srv.Handler()

	doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "sec-app",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/sec-app/secrets", map[string]any{
		"name": "db-pass",
		"data": map[string]string{"password": "s3cret"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create secret: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	w = doRequest(h, http.MethodGet, "/api/projects/default/apps/sec-app/secrets", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list secrets: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = doRequest(h, http.MethodDelete, "/api/projects/default/apps/sec-app/secrets/db-pass", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delete secret: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}
