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
// correct fields and that hasToken reflects whether the token Secret exists.
func TestListGitProvidersSummary(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()

	// Ensure mortise-system namespace exists for token secret creation.
	_ = k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"},
	})

	// Create a GitProvider that will have a token secret.
	gpWithToken := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "github-main"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitHub,
			Host: "https://github.com",
			OAuth: mortisev1alpha1.OAuthConfig{
				ClientIDSecretRef:     mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "gh-creds", Key: "clientID"},
				ClientSecretSecretRef: mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "gh-creds", Key: "clientSecret"},
			},
			WebhookSecretRef: mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "gh-webhook", Key: "secret"},
		},
	}
	if err := k8sClient.Create(ctx, gpWithToken); err != nil {
		t.Fatalf("create GitProvider github-main: %v", err)
	}

	// Create a GitProvider without a token secret.
	gpNoToken := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "gitea-internal"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitea,
			Host: "https://gitea.internal.example",
			OAuth: mortisev1alpha1.OAuthConfig{
				ClientIDSecretRef:     mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "gitea-creds", Key: "clientID"},
				ClientSecretSecretRef: mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "gitea-creds", Key: "clientSecret"},
			},
			WebhookSecretRef: mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "gitea-webhook", Key: "secret"},
		},
	}
	if err := k8sClient.Create(ctx, gpNoToken); err != nil {
		t.Fatalf("create GitProvider gitea-internal: %v", err)
	}

	// Write the token secret for github-main only.
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gitprovider-token-github-main",
			Namespace: "mortise-system",
		},
		Data: map[string][]byte{"token": []byte("ghs_test")},
	}
	if err := k8sClient.Create(ctx, tokenSecret); err != nil {
		t.Fatalf("create token secret: %v", err)
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
	if len(resp) != 2 {
		t.Fatalf("expected 2 git providers, got %d", len(resp))
	}

	// Build a lookup map for easier assertions.
	byName := make(map[string]map[string]any)
	for _, item := range resp {
		byName[item["name"].(string)] = item
	}

	gh := byName["github-main"]
	if gh["type"] != "github" {
		t.Errorf("github-main type: expected github, got %v", gh["type"])
	}
	if gh["host"] != "https://github.com" {
		t.Errorf("github-main host: expected https://github.com, got %v", gh["host"])
	}
	if gh["hasToken"] != true {
		t.Errorf("github-main hasToken: expected true, got %v", gh["hasToken"])
	}

	gt := byName["gitea-internal"]
	if gt["type"] != "gitea" {
		t.Errorf("gitea-internal type: expected gitea, got %v", gt["type"])
	}
	if gt["hasToken"] != false {
		t.Errorf("gitea-internal hasToken: expected false, got %v", gt["hasToken"])
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

// validCreateGitProviderBody returns a request body the handler accepts,
// scoped to a test-provided name so each test can keep its resources isolated.
func validCreateGitProviderBody(name string) map[string]any {
	return map[string]any{
		"name": name,
		"type": "github",
		"host": "https://github.com",
		"oauth": map[string]string{
			"clientID":     "client-id-123",
			"clientSecret": "client-secret-xyz",
		},
		"webhookSecret": "whsec_abc123",
	}
}

// TestCreateGitProviderHappyPath verifies that a valid request creates both
// the GitProvider CRD and the backing OAuth Secret with the expected shape.
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
	if resp["hasToken"] != false {
		t.Errorf("hasToken: expected false, got %v", resp["hasToken"])
	}
	if _, present := resp["clientSecret"]; present {
		t.Errorf("response must not echo clientSecret")
	}

	ctx := context.Background()

	var gp mortisev1alpha1.GitProvider
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "github-main"}, &gp); err != nil {
		t.Fatalf("get GitProvider: %v", err)
	}
	if gp.Spec.Host != "https://github.com" {
		t.Errorf("host: got %q", gp.Spec.Host)
	}
	if gp.Spec.OAuth.ClientIDSecretRef.Name != "gitprovider-oauth-github-main" {
		t.Errorf("clientIDSecretRef.Name: got %q", gp.Spec.OAuth.ClientIDSecretRef.Name)
	}
	if gp.Spec.OAuth.ClientIDSecretRef.Key != "clientID" {
		t.Errorf("clientIDSecretRef.Key: got %q", gp.Spec.OAuth.ClientIDSecretRef.Key)
	}
	if gp.Spec.WebhookSecretRef.Key != "webhookSecret" {
		t.Errorf("webhookSecretRef.Key: got %q", gp.Spec.WebhookSecretRef.Key)
	}

	var secret corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: "mortise-system",
		Name:      "gitprovider-oauth-github-main",
	}, &secret); err != nil {
		t.Fatalf("get OAuth secret: %v", err)
	}
	if string(secret.Data["clientID"]) != "client-id-123" {
		t.Errorf("secret clientID: got %q", secret.Data["clientID"])
	}
	if string(secret.Data["clientSecret"]) != "client-secret-xyz" {
		t.Errorf("secret clientSecret: got %q", secret.Data["clientSecret"])
	}
	if string(secret.Data["webhookSecret"]) != "whsec_abc123" {
		t.Errorf("secret webhookSecret: got %q", secret.Data["webhookSecret"])
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
				"type":          "github",
				"host":          "https://github.com",
				"oauth":         map[string]string{"clientID": "x", "clientSecret": "y"},
				"webhookSecret": "z",
			},
		},
		{
			name: "invalid name",
			body: map[string]any{
				"name":          "Bad Name",
				"type":          "github",
				"host":          "https://github.com",
				"oauth":         map[string]string{"clientID": "x", "clientSecret": "y"},
				"webhookSecret": "z",
			},
		},
		{
			name: "bad type",
			body: map[string]any{
				"name":          "my-provider",
				"type":          "bitbucket",
				"host":          "https://bitbucket.org",
				"oauth":         map[string]string{"clientID": "x", "clientSecret": "y"},
				"webhookSecret": "z",
			},
		},
		{
			name: "non-url host",
			body: map[string]any{
				"name":          "my-provider",
				"type":          "github",
				"host":          "not-a-url",
				"oauth":         map[string]string{"clientID": "x", "clientSecret": "y"},
				"webhookSecret": "z",
			},
		},
		{
			name: "missing clientID",
			body: map[string]any{
				"name":          "my-provider",
				"type":          "github",
				"host":          "https://github.com",
				"oauth":         map[string]string{"clientSecret": "y"},
				"webhookSecret": "z",
			},
		},
		{
			name: "missing webhookSecret",
			body: map[string]any{
				"name":  "my-provider",
				"type":  "github",
				"host":  "https://github.com",
				"oauth": map[string]string{"clientID": "x", "clientSecret": "y"},
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

			// Confirm nothing was persisted.
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

// TestDeleteGitProviderHappyPath verifies that deletion removes the CRD,
// the OAuth credentials Secret, and the OAuth access token Secret.
func TestDeleteGitProviderHappyPath(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ctx := context.Background()

	w := doRequest(h, http.MethodPost, "/api/gitproviders", validCreateGitProviderBody("github-main"))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed create: %d: %s", w.Code, w.Body.String())
	}

	// Simulate a completed OAuth flow by creating the token secret.
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gitprovider-token-github-main",
			Namespace: "mortise-system",
		},
		Data: map[string][]byte{"token": []byte("ghs_test")},
	}
	if err := k8sClient.Create(ctx, tokenSecret); err != nil {
		t.Fatalf("create token secret: %v", err)
	}

	w = doRequest(h, http.MethodDelete, "/api/gitproviders/github-main", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	var gp mortisev1alpha1.GitProvider
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "github-main"}, &gp); !errors.IsNotFound(err) {
		t.Errorf("GitProvider still present: err=%v", err)
	}

	var oauthSecret corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: "mortise-system",
		Name:      "gitprovider-oauth-github-main",
	}, &oauthSecret); !errors.IsNotFound(err) {
		t.Errorf("OAuth Secret still present: err=%v", err)
	}

	var tok corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: "mortise-system",
		Name:      "gitprovider-token-github-main",
	}, &tok); !errors.IsNotFound(err) {
		t.Errorf("token Secret still present: err=%v", err)
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

	// Create only the CRD, no managed secrets, to simulate an orphan.
	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "orphan"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitHub,
			Host: "https://github.com",
			OAuth: mortisev1alpha1.OAuthConfig{
				ClientIDSecretRef:     mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "x", Key: "clientID"},
				ClientSecretSecretRef: mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "x", Key: "clientSecret"},
			},
			WebhookSecretRef: mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "x", Key: "webhookSecret"},
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
