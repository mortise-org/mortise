package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

// gitProviderSummary is the JSON shape returned for each GitProvider.
type gitProviderSummary struct {
	Name     string                           `json:"name"`
	Type     mortisev1alpha1.GitProviderType  `json:"type"`
	Host     string                           `json:"host"`
	Phase    mortisev1alpha1.GitProviderPhase `json:"phase"`
	HasToken bool                             `json:"hasToken"`
}

// createGitProviderRequest is the JSON body for creating a GitProvider.
type createGitProviderRequest struct {
	Name          string                          `json:"name"`
	Type          mortisev1alpha1.GitProviderType `json:"type"`
	Host          string                          `json:"host"`
	OAuth         createGitProviderOAuth          `json:"oauth"`
	WebhookSecret string                          `json:"webhookSecret"`
}

type createGitProviderOAuth struct {
	ClientID     string `json:"clientID"`
	ClientSecret string `json:"clientSecret"`
}

// ListGitProviders returns all GitProvider CRDs with their connection status.
// Admin-only — git providers are platform-scoped.
//
// GET /api/gitproviders
func (s *Server) ListGitProviders(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}

	var list mortisev1alpha1.GitProviderList
	if err := s.client.List(r.Context(), &list); err != nil {
		writeError(w, err)
		return
	}

	resp := make([]gitProviderSummary, 0, len(list.Items))
	for _, gp := range list.Items {
		resp = append(resp, gitProviderSummary{
			Name:     gp.Name,
			Type:     gp.Spec.Type,
			Host:     gp.Spec.Host,
			Phase:    gp.Status.Phase,
			HasToken: s.oauthTokenExists(r.Context(), gp.Name),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// CreateGitProvider creates a new GitProvider CRD and its backing OAuth secret.
// Admin-only — git providers are platform-scoped.
//
// POST /api/gitproviders
func (s *Server) CreateGitProvider(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}

	var req createGitProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if msg := validateGitProviderRequest(&req); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{msg})
		return
	}

	// Reject duplicates up-front so we don't create an orphan Secret.
	var existing mortisev1alpha1.GitProvider
	err := s.client.Get(r.Context(), types.NamespacedName{Name: req.Name}, &existing)
	if err == nil {
		writeJSON(w, http.StatusConflict, errorResponse{"git provider " + req.Name + " already exists"})
		return
	}
	if !errors.IsNotFound(err) {
		writeError(w, err)
		return
	}

	secretName := gitProviderOAuthSecretName(req.Name)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: tokenSecretNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "mortise",
				"mortise.dev/managed-by":       "api",
				"mortise.dev/git-provider":     req.Name,
			},
		},
		Data: map[string][]byte{
			"clientID":      []byte(req.OAuth.ClientID),
			"clientSecret":  []byte(req.OAuth.ClientSecret),
			"webhookSecret": []byte(req.WebhookSecret),
		},
	}

	if err := s.client.Create(r.Context(), secret); err != nil {
		writeError(w, err)
		return
	}

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: req.Name},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: req.Type,
			Host: req.Host,
			OAuth: mortisev1alpha1.OAuthConfig{
				ClientIDSecretRef: mortisev1alpha1.SecretRef{
					Namespace: tokenSecretNamespace,
					Name:      secretName,
					Key:       "clientID",
				},
				ClientSecretSecretRef: mortisev1alpha1.SecretRef{
					Namespace: tokenSecretNamespace,
					Name:      secretName,
					Key:       "clientSecret",
				},
			},
			WebhookSecretRef: mortisev1alpha1.SecretRef{
				Namespace: tokenSecretNamespace,
				Name:      secretName,
				Key:       "webhookSecret",
			},
		},
	}

	if err := s.client.Create(r.Context(), gp); err != nil {
		// Roll back the Secret so we don't leave an orphan.
		_ = s.client.Delete(r.Context(), secret)
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, gitProviderSummary{
		Name:     gp.Name,
		Type:     gp.Spec.Type,
		Host:     gp.Spec.Host,
		Phase:    gp.Status.Phase,
		HasToken: false,
	})
}

// DeleteGitProvider deletes a GitProvider CRD along with its managed OAuth
// secret and any stored OAuth access token. Admin-only.
//
// DELETE /api/gitproviders/{name}
func (s *Server) DeleteGitProvider(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}

	name := chi.URLParam(r, "name")

	var gp mortisev1alpha1.GitProvider
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name}, &gp); err != nil {
		writeError(w, err)
		return
	}
	if err := s.client.Delete(r.Context(), &gp); err != nil {
		writeError(w, err)
		return
	}

	// Best-effort cleanup of the managed OAuth credentials Secret.
	oauthSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gitProviderOAuthSecretName(name),
			Namespace: tokenSecretNamespace,
		},
	}
	if err := s.client.Delete(r.Context(), oauthSecret); err != nil && !errors.IsNotFound(err) {
		writeError(w, err)
		return
	}

	// Best-effort cleanup of the per-provider OAuth access token Secret
	// written by the OAuth callback.
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gitprovider-token-" + name,
			Namespace: tokenSecretNamespace,
		},
	}
	if err := s.client.Delete(r.Context(), tokenSecret); err != nil && !errors.IsNotFound(err) {
		writeError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// gitProviderOAuthSecretName is the name of the Secret that holds the OAuth
// client credentials + webhook secret for a given GitProvider. API-managed.
func gitProviderOAuthSecretName(providerName string) string {
	return "gitprovider-oauth-" + providerName
}

// validateGitProviderRequest returns an error message describing why the
// request is invalid, or "" if it's acceptable.
func validateGitProviderRequest(req *createGitProviderRequest) string {
	if req.Name == "" {
		return "name is required"
	}
	if len(req.Name) > 63 {
		return "name must be 63 characters or fewer"
	}
	if !dns1123LabelRegex.MatchString(req.Name) {
		return "name must be a DNS-1123 label: lowercase alphanumerics and '-', starting and ending with an alphanumeric"
	}
	switch req.Type {
	case mortisev1alpha1.GitProviderTypeGitHub,
		mortisev1alpha1.GitProviderTypeGitLab,
		mortisev1alpha1.GitProviderTypeGitea:
	default:
		return "type must be one of: github, gitlab, gitea"
	}
	host := strings.TrimSpace(req.Host)
	if host == "" {
		return "host is required"
	}
	u, err := url.Parse(host)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "host must be an absolute URL (e.g. https://github.com)"
	}
	if req.OAuth.ClientID == "" {
		return "oauth.clientID is required"
	}
	if req.OAuth.ClientSecret == "" {
		return "oauth.clientSecret is required"
	}
	if req.WebhookSecret == "" {
		return "webhookSecret is required"
	}
	return ""
}

// oauthTokenExists returns true if the OAuth token Secret for the given
// provider exists in mortise-system. The secret is named
// "gitprovider-token-{name}" per the storeToken convention in oauth.go.
func (s *Server) oauthTokenExists(ctx context.Context, providerName string) bool {
	var secret corev1.Secret
	err := s.client.Get(ctx, types.NamespacedName{
		Namespace: tokenSecretNamespace,
		Name:      "gitprovider-token-" + providerName,
	}, &secret)
	return err == nil || !errors.IsNotFound(err)
}
