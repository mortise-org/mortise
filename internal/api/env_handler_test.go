package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
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

func TestGetSharedVarsMissingProject(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/projects/ghost/shared-vars", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing project, got %d: %s", w.Code, w.Body.String())
	}
}
