package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

func seedTokenApp(t *testing.T, k8sClient client.Client, projectNS, appName string, envs ...mortisev1alpha1.Environment) {
	t.Helper()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: projectNS},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
		},
	}
	if len(envs) > 0 {
		app.Spec.Environments = envs
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}
}

func TestTokenCRUDHappyPath(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")
	seedTokenApp(t, k8sClient, ns, "webapp")

	// Create token.
	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/webapp/tokens", map[string]any{
		"name":        "ci-deploy",
		"environment": "production",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create token: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created map[string]any
	_ = json.NewDecoder(w.Body).Decode(&created)
	if created["token"] == nil || created["token"] == "" {
		t.Fatal("expected token in response")
	}
	if created["name"] != "ci-deploy" {
		t.Errorf("expected name ci-deploy, got %v", created["name"])
	}

	// List tokens.
	w = doRequest(h, http.MethodGet, "/api/projects/default/apps/webapp/tokens", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list tokens: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var tokens []map[string]any
	_ = json.NewDecoder(w.Body).Decode(&tokens)
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(tokens))
	}
	if tokens[0]["token"] != nil && tokens[0]["token"] != "" {
		t.Error("list should not expose raw token value")
	}

	// Delete token.
	w = doRequest(h, http.MethodDelete, "/api/projects/default/apps/webapp/tokens/ci-deploy", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delete token: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// List should be empty.
	w = doRequest(h, http.MethodGet, "/api/projects/default/apps/webapp/tokens", nil)
	_ = json.NewDecoder(w.Body).Decode(&tokens)
	if len(tokens) != 0 {
		t.Errorf("expected 0 tokens after delete, got %d", len(tokens))
	}
}

func TestCreateTokenMissingName(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/anything/tokens", map[string]any{
		"environment": "production",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing name, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateTokenMissingEnvironment(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/anything/tokens", map[string]any{
		"name": "ci-deploy",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing environment, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateTokenInvalidJSON(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequestRawBody(h, http.MethodPost, "/api/projects/default/apps/anything/tokens", "{bad json", testToken)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateTokenInvalidName(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")
	seedTokenApp(t, k8sClient, ns, "webapp")

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/webapp/tokens", map[string]any{
		"name":        "CI Deploy",
		"environment": "production",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid name, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateTokenAllowsDeclaredEnvWithoutAppOverride(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default", "staging", "production")
	seedTokenApp(t, k8sClient, ns, "webapp")

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/webapp/tokens", map[string]any{
		"name":        "ci-staging",
		"environment": "staging",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for declared env without override, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeployTokenCanDeployToDeclaredEnvWithoutExplicitAppOverride(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default", "staging", "production")
	seedTokenApp(t, k8sClient, ns, "env-deploy-target")

	create := doRequest(h, http.MethodPost, "/api/projects/default/apps/env-deploy-target/tokens", map[string]any{
		"name":        "ci-staging-deploy",
		"environment": "staging",
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create token: expected 201, got %d: %s", create.Code, create.Body.String())
	}
	var created struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(create.Body).Decode(&created); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	if created.Token == "" {
		t.Fatal("expected raw deploy token")
	}

	deploy := doRequestWithToken(h, http.MethodPost, "/api/projects/default/apps/env-deploy-target/deploy", map[string]any{
		"environment": "staging",
		"image":       "nginx:1.27.0",
	}, created.Token)
	if deploy.Code != http.StatusOK {
		t.Fatalf("deploy with token: expected 200, got %d: %s", deploy.Code, deploy.Body.String())
	}

	var app mortisev1alpha1.App
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "env-deploy-target", Namespace: ns}, &app); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if len(app.Spec.Environments) != 1 || app.Spec.Environments[0].Name != "staging" || app.Spec.Environments[0].Image != "nginx:1.27.0" {
		t.Fatalf("expected staging override to be created by deploy token, got %+v", app.Spec.Environments)
	}
}

func TestDeleteTokenNotFound(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodDelete, "/api/projects/default/apps/anything/tokens/ghost", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for nonexistent token, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateTokenMissingProject(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/projects/ghost/apps/anything/tokens", map[string]any{
		"name":        "ci",
		"environment": "production",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing project, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateTokenMissingAppReturns404(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/ghost/tokens", map[string]any{
		"name":        "ci",
		"environment": "production",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing app, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateTokenRejectsUndeclaredEnvironment(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default", "production")
	seedTokenApp(t, k8sClient, ns, "webapp")

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/webapp/tokens", map[string]any{
		"name":        "ci",
		"environment": "staging",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for undeclared env, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateTokenRejectsDisabledAppEnvironment(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default", "production", "staging")
	disabled := false
	seedTokenApp(t, k8sClient, ns, "webapp", mortisev1alpha1.Environment{Name: "staging", Enabled: &disabled})

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/webapp/tokens", map[string]any{
		"name":        "ci",
		"environment": "staging",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for disabled app env, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListTokensMissingAppReturns404(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/ghost/tokens", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing app, got %d: %s", w.Code, w.Body.String())
	}
}
