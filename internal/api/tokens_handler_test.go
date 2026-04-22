package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

func TestTokenCRUDHappyPath(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

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
