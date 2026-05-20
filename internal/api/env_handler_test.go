package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/api"
	"github.com/mortise-org/mortise/internal/auth"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/envstore"
)

// seedAppForEnv creates a project and app for env handler tests.
func seedAppForEnv(t *testing.T, h http.Handler) {
	t.Helper()
	doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "webapp",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})
}

func TestGetEnvHappyPath(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")
	seedAppForEnv(t, h)

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/webapp/env?environment=production", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetEnvMissingEnvParam(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")
	seedAppForEnv(t, h)

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/webapp/env", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing env param, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetEnvUndeclaredEnv(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")
	seedAppForEnv(t, h)

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/webapp/env?environment=ghost", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for undeclared env, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetEnvNonexistentApp(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/ghost/env?environment=production", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for nonexistent app, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetEnvMissingProject(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/projects/ghost/apps/anything/env?environment=production", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing project, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPutEnvInvalidJSON(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")
	seedAppForEnv(t, h)

	w := doRequestRawBody(h, http.MethodPut, "/api/projects/default/apps/webapp/env?environment=production", "{bad json", testToken)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPatchEnvInvalidJSON(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")
	seedAppForEnv(t, h)

	w := doRequestRawBody(h, http.MethodPatch, "/api/projects/default/apps/webapp/env?environment=production", "{bad json", testToken)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPutEnvRoundTrip(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")
	seedAppForEnv(t, h)

	vars := []map[string]string{
		{"name": "PORT", "value": "3000"},
		{"name": "NODE_ENV", "value": "production"},
	}
	w := doRequest(h, http.MethodPut, "/api/projects/default/apps/webapp/env?environment=production", vars)
	if w.Code != http.StatusOK {
		t.Fatalf("put: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = doRequest(h, http.MethodGet, "/api/projects/default/apps/webapp/env?environment=production", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got []map[string]any
	_ = json.NewDecoder(w.Body).Decode(&got)
	if len(got) != 2 {
		t.Fatalf("expected 2 vars, got %d", len(got))
	}
}

func TestPatchEnvSetAndUnset(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")
	seedAppForEnv(t, h)

	// Seed initial vars.
	doRequest(h, http.MethodPut, "/api/projects/default/apps/webapp/env?environment=production", []map[string]string{
		{"name": "KEEP", "value": "yes"},
		{"name": "REMOVE", "value": "bye"},
	})

	// Patch: add one, remove one.
	w := doRequest(h, http.MethodPatch, "/api/projects/default/apps/webapp/env?environment=production", map[string]any{
		"set":   map[string]string{"NEW": "added"},
		"unset": []string{"REMOVE"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("patch: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = doRequest(h, http.MethodGet, "/api/projects/default/apps/webapp/env?environment=production", nil)
	var got []map[string]any
	_ = json.NewDecoder(w.Body).Decode(&got)

	names := map[string]bool{}
	for _, v := range got {
		names[v["name"].(string)] = true
	}
	if !names["KEEP"] {
		t.Error("expected KEEP to remain")
	}
	if !names["NEW"] {
		t.Error("expected NEW to be added")
	}
	if names["REMOVE"] {
		t.Error("expected REMOVE to be unset")
	}
}

func TestImportEnvHappyPath(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")
	seedAppForEnv(t, h)

	w := doRequestRawBody(h, http.MethodPost,
		"/api/projects/default/apps/webapp/env/import?environment=production",
		"PORT=3000\nDB_HOST=localhost\n", testToken)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["count"] != "2" {
		t.Errorf("expected count 2, got %v", resp["count"])
	}
}

func TestImportEnvMissingEnvParam(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")
	seedAppForEnv(t, h)

	w := doRequestRawBody(h, http.MethodPost,
		"/api/projects/default/apps/webapp/env/import",
		"PORT=3000\n", testToken)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing env param, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetSharedVarsHappyPath(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodGet, "/api/projects/default/shared-vars", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPutSharedVarsInvalidJSON(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequestRawBody(h, http.MethodPut, "/api/projects/default/shared-vars", "{bad json", testToken)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPutSharedVarsRoundTrip(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	// Create an app so pokeAppForReconcile has something to poke.
	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	vars := []map[string]string{
		{"name": "SHARED_KEY", "value": "shared_val"},
	}
	w := doRequest(h, http.MethodPut, "/api/projects/default/shared-vars", vars)
	if w.Code != http.StatusOK {
		t.Fatalf("put: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = doRequest(h, http.MethodGet, "/api/projects/default/shared-vars", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got []map[string]any
	_ = json.NewDecoder(w.Body).Decode(&got)
	if len(got) != 1 || got[0]["name"] != "SHARED_KEY" {
		t.Errorf("expected SHARED_KEY, got %+v", got)
	}
}

func TestPatchEnvRejectsManagedVarOverwrite(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "bound-app", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source:       mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{{Name: "production"}},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	envNs := constants.EnvNamespace("default", "production")
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envNs}})

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      envstore.AppEnvSecretName("bound-app"),
			Namespace: envNs,
			Annotations: map[string]string{
				"mortise.dev/binding-keys": "DB_URL",
			},
		},
		Data: map[string][]byte{
			"DB_URL":   []byte("postgres://localhost/db"),
			"APP_PORT": []byte("3000"),
		},
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("seed env secret: %v", err)
	}

	w := doRequest(h, http.MethodPatch, "/api/projects/default/apps/bound-app/env?environment=production", map[string]any{
		"set": map[string]string{"DB_URL": "postgres://evil/db"},
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 when overwriting binding var, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPutEnvRejectsManagedVarOverwrite(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "put-bound-app", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source:       mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{{Name: "production"}},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	envNs := constants.EnvNamespace("default", "production")
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envNs}})

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      envstore.AppEnvSecretName("put-bound-app"),
			Namespace: envNs,
			Annotations: map[string]string{
				"mortise.dev/binding-keys": "DB_URL",
			},
		},
		Data: map[string][]byte{
			"DB_URL":   []byte("postgres://localhost/db"),
			"APP_PORT": []byte("3000"),
		},
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("seed env secret: %v", err)
	}

	w := doRequest(h, http.MethodPut, "/api/projects/default/apps/put-bound-app/env?environment=production", []map[string]string{
		{"name": "DB_URL", "value": "postgres://evil/db"},
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 when overwriting binding var via PUT, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPatchEnvRejectsManagedVarUnset(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "unset-app", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source:       mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{{Name: "production"}},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	envNs := constants.EnvNamespace("default", "production")
	_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envNs}})

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      envstore.AppEnvSecretName("unset-app"),
			Namespace: envNs,
			Annotations: map[string]string{
				"mortise.dev/generated-keys": "SECRET_KEY",
			},
		},
		Data: map[string][]byte{
			"SECRET_KEY": []byte("generated-value"),
			"USER_VAR":   []byte("can-delete"),
		},
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("seed env secret: %v", err)
	}

	w := doRequest(h, http.MethodPatch, "/api/projects/default/apps/unset-app/env?environment=production", map[string]any{
		"unset": []string{"SECRET_KEY"},
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 when unsetting generated var, got %d: %s", w.Code, w.Body.String())
	}

	w = doRequest(h, http.MethodPatch, "/api/projects/default/apps/unset-app/env?environment=production", map[string]any{
		"unset": []string{"USER_VAR"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 when unsetting user var, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetEnvAndSharedVarsRedactionForDeveloperAndViewer(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	ns := seedProject(t, k8sClient, "default", "production", "staging")
	seedImageApp(t, k8sClient, ns, "webapp", mortisev1alpha1.Environment{Name: "production"}, mortisev1alpha1.Environment{Name: "staging"})

	newServerForUser := func(t *testing.T, email string, role auth.Role) (*api.Server, string) {
		t.Helper()
		authProvider := auth.NewNativeAuthProvider(k8sClient)
		jwtHelper := auth.NewJWTHelper(k8sClient)
		if err := authProvider.CreateUser(ctx, email, "testpass", role); err != nil {
			t.Fatalf("create user %s: %v", email, err)
		}
		principal, err := authProvider.Authenticate(ctx, auth.Credentials{Email: email, Password: "testpass"})
		if err != nil {
			t.Fatalf("authenticate %s: %v", email, err)
		}
		token, err := jwtHelper.GenerateToken(ctx, principal)
		if err != nil {
			t.Fatalf("token for %s: %v", email, err)
		}
		return api.NewServer(k8sClient, nil, nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient)), token
	}

	var project mortisev1alpha1.Project
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: "default"}, &project); err != nil {
		t.Fatalf("get project: %v", err)
	}
	project.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{
		{Name: "production", Restricted: true},
		{Name: "staging"},
	}
	if err := k8sClient.Update(ctx, &project); err != nil {
		t.Fatalf("update project restrictions: %v", err)
	}

	store := &envstore.Store{Client: k8sClient}
	if err := store.Set(ctx, constants.EnvNamespace("default", "production"), "webapp", []envstore.Env{{Name: "DB_URL", Value: "postgres://prod", Source: "user"}}, nil); err != nil {
		t.Fatalf("seed prod env: %v", err)
	}
	if err := store.Set(ctx, constants.EnvNamespace("default", "staging"), "webapp", []envstore.Env{{Name: "DB_URL", Value: "postgres://staging", Source: "user"}}, nil); err != nil {
		t.Fatalf("seed staging env: %v", err)
	}
	if err := store.SetSharedSource(ctx, constants.ControlNamespace("default"), []envstore.Env{{Name: "SHARED_KEY", Value: "shared-value", Source: "shared"}}, nil); err != nil {
		t.Fatalf("seed shared vars: %v", err)
	}

	adminSrv, adminToken := newServerForUser(t, "admin@example.com", auth.RoleAdmin)
	developerSrv, developerToken := newServerForUser(t, "developer@example.com", auth.RoleMember)
	viewerSrv, viewerToken := newServerForUser(t, "viewer@example.com", auth.RoleMember)
	seedProjectMember(t, k8sClient, "default", "developer@example.com", mortisev1alpha1.ProjectRoleDeveloper)
	seedProjectMember(t, k8sClient, "default", "viewer@example.com", mortisev1alpha1.ProjectRoleViewer)

	type envVar struct {
		Name     string `json:"name"`
		Value    string `json:"value"`
		Source   string `json:"source"`
		Redacted bool   `json:"redacted"`
	}
	readVars := func(t *testing.T, h http.Handler, token, path string) []envVar {
		t.Helper()
		w := doRequestWithToken(h, http.MethodGet, path, nil, token)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: expected 200, got %d: %s", path, w.Code, w.Body.String())
		}
		var got []envVar
		if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		return got
	}

	adminProd := readVars(t, adminSrv.Handler(), adminToken, "/api/projects/default/apps/webapp/env?environment=production")
	if len(adminProd) != 1 || adminProd[0].Value != "postgres://prod" || adminProd[0].Redacted {
		t.Fatalf("admin should see plaintext production env, got %+v", adminProd)
	}

	developerStaging := readVars(t, developerSrv.Handler(), developerToken, "/api/projects/default/apps/webapp/env?environment=staging")
	if len(developerStaging) != 1 || developerStaging[0].Value != "postgres://staging" || developerStaging[0].Redacted {
		t.Fatalf("developer should see plaintext staging env, got %+v", developerStaging)
	}

	developerProd := readVars(t, developerSrv.Handler(), developerToken, "/api/projects/default/apps/webapp/env?environment=production")
	if len(developerProd) != 1 || developerProd[0].Value != "" || !developerProd[0].Redacted || developerProd[0].Source != "user" {
		t.Fatalf("developer should receive redacted production env metadata, got %+v", developerProd)
	}

	developerShared := readVars(t, developerSrv.Handler(), developerToken, "/api/projects/default/shared-vars")
	if len(developerShared) != 1 || developerShared[0].Value != "shared-value" || developerShared[0].Redacted {
		t.Fatalf("developer should see plaintext shared vars, got %+v", developerShared)
	}

	viewerStaging := readVars(t, viewerSrv.Handler(), viewerToken, "/api/projects/default/apps/webapp/env?environment=staging")
	if len(viewerStaging) != 1 || viewerStaging[0].Value != "" || !viewerStaging[0].Redacted {
		t.Fatalf("viewer should receive redacted app env metadata, got %+v", viewerStaging)
	}

	viewerShared := readVars(t, viewerSrv.Handler(), viewerToken, "/api/projects/default/shared-vars")
	if len(viewerShared) != 1 || viewerShared[0].Value != "" || !viewerShared[0].Redacted {
		t.Fatalf("viewer should receive redacted shared var metadata, got %+v", viewerShared)
	}
}

func TestGetSharedVarsMissingProject(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/projects/ghost/shared-vars", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing project, got %d: %s", w.Code, w.Body.String())
	}
}
