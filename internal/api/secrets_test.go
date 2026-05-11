package api_test

import (
	"net/http"
	"testing"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

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
