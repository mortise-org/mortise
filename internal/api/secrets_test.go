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

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/envstore"
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

func TestCreateSecretRejectsReservedRuntimeSecretName(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "reserved-secret-create")
	seedImageApp(t, k8sClient, ns, "reserved-app")

	for _, name := range []string{envstore.AppEnvSecretName("reserved-app"), "reserved-app-pull-secret"} {
		w := doRequest(h, http.MethodPost, "/api/projects/reserved-secret-create/apps/reserved-app/secrets?environment=production", map[string]any{
			"name": name,
			"data": map[string]string{"TOP": "secret"},
		})
		if w.Code != http.StatusConflict {
			t.Fatalf("create reserved secret %q: expected 409, got %d: %s", name, w.Code, w.Body.String())
		}
	}
}

func TestListSecretsHidesReservedRuntimeSecrets(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "reserved-secret-list")
	seedImageApp(t, k8sClient, ns, "reserved-app")

	create := doRequest(h, http.MethodPost, "/api/projects/reserved-secret-list/apps/reserved-app/secrets?environment=production", map[string]any{
		"name": "user-secret",
		"data": map[string]string{"TOP": "secret"},
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create user secret: expected 201, got %d: %s", create.Code, create.Body.String())
	}

	setPull := doRequest(h, http.MethodPost, "/api/projects/reserved-secret-list/apps/reserved-app/pull-credentials", map[string]any{
		"registry": "ghcr.io",
		"username": "octo",
		"password": "secret",
	})
	if setPull.Code != http.StatusOK {
		t.Fatalf("set pull credentials: expected 200, got %d: %s", setPull.Code, setPull.Body.String())
	}

	w := doRequest(h, http.MethodGet, "/api/projects/reserved-secret-list/apps/reserved-app/secrets?environment=production", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list secrets: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode secrets response: %v", err)
	}
	if len(resp) != 1 || resp[0]["name"] != "user-secret" {
		t.Fatalf("expected only user-secret in response, got %+v", resp)
	}
}

func TestDeleteSecretRejectsReservedPullSecret(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "reserved-secret-delete")
	seedImageApp(t, k8sClient, ns, "reserved-app")

	setPull := doRequest(h, http.MethodPost, "/api/projects/reserved-secret-delete/apps/reserved-app/pull-credentials", map[string]any{
		"registry": "ghcr.io",
		"username": "octo",
		"password": "secret",
	})
	if setPull.Code != http.StatusOK {
		t.Fatalf("set pull credentials: expected 200, got %d: %s", setPull.Code, setPull.Body.String())
	}

	w := doRequest(h, http.MethodDelete, "/api/projects/reserved-secret-delete/apps/reserved-app/secrets/reserved-app-pull-secret?environment=production", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("delete reserved pull secret: expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSecretsRejectUndeclaredEnvironment(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")
	seedImageApp(t, k8sClient, ns, "secret-app")

	tests := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{
			name:   "create",
			method: http.MethodPost,
			path:   "/api/projects/default/apps/secret-app/secrets?env=staging",
			body:   map[string]any{"name": "db-creds", "data": map[string]string{"password": "s3cret"}},
		},
		{
			name:   "list",
			method: http.MethodGet,
			path:   "/api/projects/default/apps/secret-app/secrets?env=staging",
		},
		{
			name:   "delete",
			method: http.MethodDelete,
			path:   "/api/projects/default/apps/secret-app/secrets/db-creds?env=staging",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := doRequest(h, tc.method, tc.path, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for undeclared env, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSecretsRejectDisabledAppEnvironment(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default", "staging", "production")
	disabled := false
	seedImageApp(t, k8sClient, ns, "secret-app", mortisev1alpha1.Environment{Name: "staging", Enabled: &disabled})

	tests := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{
			name:   "create",
			method: http.MethodPost,
			path:   "/api/projects/default/apps/secret-app/secrets?env=staging",
			body:   map[string]any{"name": "db-creds", "data": map[string]string{"password": "s3cret"}},
		},
		{
			name:   "list",
			method: http.MethodGet,
			path:   "/api/projects/default/apps/secret-app/secrets?env=staging",
		},
		{
			name:   "delete",
			method: http.MethodDelete,
			path:   "/api/projects/default/apps/secret-app/secrets/db-creds?env=staging",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := doRequest(h, tc.method, tc.path, tc.body)
			if w.Code != http.StatusNotFound {
				t.Fatalf("expected 404 for disabled env, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}
