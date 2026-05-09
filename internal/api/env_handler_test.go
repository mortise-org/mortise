package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
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

func TestGetSharedVarsMissingProject(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/projects/ghost/shared-vars", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing project, got %d: %s", w.Code, w.Body.String())
	}
}
