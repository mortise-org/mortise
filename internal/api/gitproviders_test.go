package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/auth"
)

// TestListGitProvidersEmpty verifies that an empty list is returned when no
// GitProviders are configured.
func TestListGitProvidersEmpty(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/gitproviders", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp) != 0 {
		t.Errorf("expected empty list, got %d items", len(resp))
	}
}

// TestListGitProvidersSummary verifies that GitProviders are listed with the
// correct fields.
func TestListGitProvidersSummary(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()

	_ = k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"},
	})

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "github-main"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type:     mortisev1alpha1.GitProviderTypeGitHub,
			Host:     "https://github.com",
			ClientID: "test-client-id",
		},
	}
	if err := k8sClient.Create(ctx, gp); err != nil {
		t.Fatalf("create GitProvider: %v", err)
	}

	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/gitproviders", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp) != 1 {
		t.Fatalf("expected 1 git provider, got %d", len(resp))
	}

	gh := resp[0]
	if gh["name"] != "github-main" {
		t.Errorf("name: expected github-main, got %v", gh["name"])
	}
	if gh["type"] != "github" {
		t.Errorf("type: expected github, got %v", gh["type"])
	}
	if gh["host"] != "https://github.com" {
		t.Errorf("host: expected https://github.com, got %v", gh["host"])
	}
}

// TestListGitProvidersForbiddenForMember verifies that non-admin users cannot
// list git providers.
func TestListGitProvidersForbiddenForMember(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, _ := newTestServerAs(t, k8sClient, auth.RoleMember)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/gitproviders", nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// validCreateGitProviderBody returns a request body the handler accepts.
func validCreateGitProviderBody(name string) map[string]any {
	return map[string]any{
		"name":     name,
		"type":     "github",
		"host":     "https://github.com",
		"clientID": "client-id-123",
	}
}

// TestCreateGitProviderHappyPath verifies that a valid request creates the
// GitProvider CRD and an auto-generated webhook secret.
func TestCreateGitProviderHappyPath(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/gitproviders", validCreateGitProviderBody("github-main"))
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["name"] != "github-main" {
		t.Errorf("name: expected github-main, got %v", resp["name"])
	}
	if resp["type"] != "github" {
		t.Errorf("type: expected github, got %v", resp["type"])
	}

	ctx := context.Background()

	var gp mortisev1alpha1.GitProvider
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "github-main"}, &gp); err != nil {
		t.Fatalf("get GitProvider: %v", err)
	}
	if gp.Spec.Host != "https://github.com" {
		t.Errorf("host: got %q", gp.Spec.Host)
	}
	if gp.Spec.ClientID != "client-id-123" {
		t.Errorf("clientID: got %q", gp.Spec.ClientID)
	}
	if gp.Spec.WebhookSecretRef == nil {
		t.Fatal("webhookSecretRef is nil")
	}
	if gp.Spec.WebhookSecretRef.Key != "webhookSecret" {
		t.Errorf("webhookSecretRef.Key: got %q", gp.Spec.WebhookSecretRef.Key)
	}

	// Verify the auto-generated webhook secret exists.
	var secret corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: "mortise-system",
		Name:      "gitprovider-webhook-github-main",
	}, &secret); err != nil {
		t.Fatalf("get webhook secret: %v", err)
	}
	if len(secret.Data["webhookSecret"]) == 0 {
		t.Error("webhook secret is empty")
	}
	if secret.Labels["mortise.dev/managed-by"] != "api" {
		t.Errorf("label mortise.dev/managed-by: got %q", secret.Labels["mortise.dev/managed-by"])
	}
}

// TestCreateGitProviderValidation verifies that invalid payloads are rejected
// with 400 before any resources are created.
func TestCreateGitProviderValidation(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
	}{
		{
			name: "missing name",
			body: map[string]any{
				"type": "github",
				"host": "https://github.com",
			},
		},
		{
			name: "invalid name",
			body: map[string]any{
				"name": "Bad Name",
				"type": "github",
				"host": "https://github.com",
			},
		},
		{
			name: "bad type",
			body: map[string]any{
				"name": "my-provider",
				"type": "bitbucket",
				"host": "https://bitbucket.org",
			},
		},
		{
			name: "non-url host",
			body: map[string]any{
				"name": "my-provider",
				"type": "github",
				"host": "not-a-url",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k8sClient := setupEnvtest(t)
			srv := newAdminServer(t, k8sClient)
			h := srv.Handler()

			w := doRequest(h, http.MethodPost, "/api/gitproviders", tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}

			var list mortisev1alpha1.GitProviderList
			if err := k8sClient.List(context.Background(), &list); err != nil {
				t.Fatalf("list GitProviders: %v", err)
			}
			if len(list.Items) != 0 {
				t.Errorf("expected no GitProviders, got %d", len(list.Items))
			}
		})
	}
}

// TestCreateGitProviderConflict verifies that creating a provider with a name
// that already exists returns 409.
func TestCreateGitProviderConflict(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/gitproviders", validCreateGitProviderBody("github-main"))
	if w.Code != http.StatusCreated {
		t.Fatalf("first create: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	w = doRequest(h, http.MethodPost, "/api/gitproviders", validCreateGitProviderBody("github-main"))
	if w.Code != http.StatusConflict {
		t.Fatalf("second create: expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCreateGitProviderForbiddenForMember verifies that non-admin users cannot
// create git providers.
func TestCreateGitProviderForbiddenForMember(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, _ := newTestServerAs(t, k8sClient, auth.RoleMember)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/gitproviders", validCreateGitProviderBody("github-main"))
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDeleteGitProviderHappyPath verifies that deletion removes the CRD and
// the managed webhook secret.
func TestDeleteGitProviderHappyPath(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ctx := context.Background()

	w := doRequest(h, http.MethodPost, "/api/gitproviders", validCreateGitProviderBody("github-main"))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed create: %d: %s", w.Code, w.Body.String())
	}

	w = doRequest(h, http.MethodDelete, "/api/gitproviders/github-main", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	var gp mortisev1alpha1.GitProvider
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "github-main"}, &gp); !errors.IsNotFound(err) {
		t.Errorf("GitProvider still present: err=%v", err)
	}

	var whSecret corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: "mortise-system",
		Name:      "gitprovider-webhook-github-main",
	}, &whSecret); !errors.IsNotFound(err) {
		t.Errorf("webhook Secret still present: err=%v", err)
	}
}

// TestDeleteGitProviderNotFound verifies that deleting an unknown provider
// returns 404.
func TestDeleteGitProviderNotFound(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodDelete, "/api/gitproviders/ghost", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDeleteGitProviderIgnoresMissingSecrets verifies that delete still
// succeeds even if the managed Secrets are already gone.
func TestDeleteGitProviderIgnoresMissingSecrets(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()

	_ = k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"},
	})

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "orphan"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type:     mortisev1alpha1.GitProviderTypeGitHub,
			Host:     "https://github.com",
			ClientID: "test-id",
		},
	}
	if err := k8sClient.Create(ctx, gp); err != nil {
		t.Fatalf("seed orphan GitProvider: %v", err)
	}

	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodDelete, "/api/gitproviders/orphan", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDeleteGitProviderForbiddenForMember verifies that non-admin users cannot
// delete git providers.
func TestDeleteGitProviderForbiddenForMember(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, _ := newTestServerAs(t, k8sClient, auth.RoleMember)
	h := srv.Handler()

	w := doRequest(h, http.MethodDelete, "/api/gitproviders/whatever", nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}
