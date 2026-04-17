package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/git"
)

// TestListReposMissingProvider verifies that omitting the provider query param
// returns 400.
func TestListReposMissingProvider(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/repos", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestListReposProviderNotFound verifies that requesting repos for a
// non-existent provider returns an error.
func TestListReposProviderNotFound(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/repos?provider=ghost", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestListReposNoToken verifies that a provider without a stored token
// returns an error.
func TestListReposNoToken(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()
	_ = k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"},
	})

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "gh-no-token"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitHub,
			Host: "https://github.com",
			OAuth: mortisev1alpha1.OAuthConfig{
				ClientIDSecretRef:     mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "x", Key: "id"},
				ClientSecretSecretRef: mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "x", Key: "secret"},
			},
			WebhookSecretRef: mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "x", Key: "wh"},
		},
	}
	if err := k8sClient.Create(ctx, gp); err != nil {
		t.Fatalf("create GitProvider: %v", err)
	}

	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/repos?provider=gh-no-token", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestListBranchesMissingProvider verifies that omitting the provider query
// param returns 400.
func TestListBranchesMissingProvider(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/repos/octo/myrepo/branches", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestListReposRequiresAuth verifies that unauthenticated requests are
// rejected with 401.
func TestListReposRequiresAuth(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodGet, "/api/repos?provider=x", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// TestListBranchesRequiresAuth verifies that unauthenticated requests are
// rejected with 401.
func TestListBranchesRequiresAuth(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodGet, "/api/repos/o/r/branches?provider=x", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// Verify Repository and Branch types round-trip through JSON correctly.
func TestRepositoryJSON(t *testing.T) {
	repo := git.Repository{
		FullName:      "octo/app",
		Name:          "app",
		Description:   "My app",
		DefaultBranch: "main",
		CloneURL:      "https://github.com/octo/app.git",
		UpdatedAt:     "2025-01-01T00:00:00Z",
		Language:      "Go",
		Private:       true,
	}
	data, _ := json.Marshal(repo)
	var got git.Repository
	_ = json.Unmarshal(data, &got)
	if got.FullName != repo.FullName || got.Private != repo.Private {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
}

func TestBranchJSON(t *testing.T) {
	b := git.Branch{Name: "main", Default: true}
	data, _ := json.Marshal(b)
	var got git.Branch
	_ = json.Unmarshal(data, &got)
	if got.Name != "main" || !got.Default {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
}
