package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mortise-org/mortise/internal/constants"
)

func TestCreateSecretLabelsProject(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")
	seedAppForEnv(t, h)

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/webapp/secrets?environment=production", map[string]any{
		"name": "my-secret",
		"data": map[string]string{"TOP": "secret"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create secret: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var got corev1.Secret
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Namespace: constants.EnvNamespace("default", "production"),
		Name:      "my-secret",
	}, &got); err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if got.Labels[constants.ProjectLabel] != "default" {
		t.Fatalf("expected project label %q, got %q", "default", got.Labels[constants.ProjectLabel])
	}
}

func TestListSecretsRoundTrip(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")
	seedAppForEnv(t, h)

	doRequest(h, http.MethodPost, "/api/projects/default/apps/webapp/secrets?environment=production", map[string]any{
		"name": "my-secret",
		"data": map[string]string{"TOP": "secret"},
	})

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/webapp/secrets?environment=production", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list secrets: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode secrets response: %v", err)
	}
	if len(resp) != 1 {
		t.Fatalf("expected only the user secret in response, got %+v", resp)
	}
	if resp[0]["name"] != "my-secret" {
		t.Fatalf("expected my-secret in response, got %+v", resp)
	}
}

func TestSecretEndpointsRequireExistingApp(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	create := doRequest(h, http.MethodPost, "/api/projects/default/apps/ghost/secrets?environment=production", map[string]any{
		"name": "my-secret",
		"data": map[string]string{"TOP": "secret"},
	})
	if create.Code != http.StatusNotFound {
		t.Fatalf("create secret: expected 404 for missing app, got %d: %s", create.Code, create.Body.String())
	}

	list := doRequest(h, http.MethodGet, "/api/projects/default/apps/ghost/secrets?environment=production", nil)
	if list.Code != http.StatusNotFound {
		t.Fatalf("list secrets: expected 404 for missing app, got %d: %s", list.Code, list.Body.String())
	}
}

func TestDeleteSecretRejectsInternalEnvSecret(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")
	seedAppForEnv(t, h)
	if err := k8sClient.Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "webapp-env",
			Namespace: constants.EnvNamespace("default", "production"),
			Labels: map[string]string{
				constants.AppNameLabel:         "webapp",
				constants.ProjectLabel:         "default",
				"app.kubernetes.io/managed-by": "mortise",
			},
		},
	}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("seed internal env secret: %v", err)
	}

	w := doRequest(h, http.MethodDelete, "/api/projects/default/apps/webapp/secrets/webapp-env?environment=production", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("delete internal env secret: expected 403, got %d: %s", w.Code, w.Body.String())
	}
}
