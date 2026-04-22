package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/git"
)

// gitProviderSummary is the JSON shape returned for each GitProvider.
type gitProviderSummary struct {
	Name  string                           `json:"name"`
	Type  mortisev1alpha1.GitProviderType  `json:"type"`
	Host  string                           `json:"host"`
	Phase mortisev1alpha1.GitProviderPhase `json:"phase"`
}

// createGitProviderRequest is the JSON body for creating a GitProvider.
type createGitProviderRequest struct {
	Name     string                          `json:"name"`
	Type     mortisev1alpha1.GitProviderType `json:"type"`
	Host     string                          `json:"host"`
	ClientID string                          `json:"clientID"`
}

// ListGitProviders returns all GitProvider CRDs.
// Admin-only — git providers are platform-scoped.
//
// GET /api/gitproviders
func (s *Server) ListGitProviders(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, authz.Resource{Kind: "gitprovider"}, authz.ActionRead) {
		return
	}

	var list mortisev1alpha1.GitProviderList
	if err := s.client.List(r.Context(), &list); err != nil {
		writeError(w, err)
		return
	}

	resp := make([]gitProviderSummary, 0, len(list.Items))
	for _, gp := range list.Items {
		summary := gitProviderSummary{
			Name:  gp.Name,
			Type:  gp.Spec.Type,
			Host:  gp.Spec.Host,
			Phase: gp.Status.Phase,
		}
		resp = append(resp, summary)
	}
	writeJSON(w, http.StatusOK, resp)
}

// CreateGitProvider creates a new GitProvider CRD with an auto-generated
// webhook secret. Admin-only — git providers are platform-scoped.
//
// POST /api/gitproviders
func (s *Server) CreateGitProvider(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, authz.Resource{Kind: "gitprovider"}, authz.ActionCreate) {
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

	// Reject duplicates.
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

	// Auto-generate webhook secret.
	webhookSecretBytes := make([]byte, 32)
	if _, err := rand.Read(webhookSecretBytes); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"failed to generate webhook secret"})
		return
	}
	webhookSecretValue := hex.EncodeToString(webhookSecretBytes)

	secretName := webhookSecretName(req.Name)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: git.TokenSecretNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "mortise",
				"mortise.dev/managed-by":       "api",
				"mortise.dev/git-provider":     req.Name,
			},
		},
		Data: map[string][]byte{
			"webhookSecret": []byte(webhookSecretValue),
		},
	}

	if err := s.client.Create(r.Context(), secret); err != nil {
		writeError(w, err)
		return
	}

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: req.Name},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type:     req.Type,
			Host:     req.Host,
			ClientID: req.ClientID,
			WebhookSecretRef: &mortisev1alpha1.SecretRef{
				Namespace: git.TokenSecretNamespace,
				Name:      secretName,
				Key:       "webhookSecret",
			},
		},
	}

	if err := s.client.Create(r.Context(), gp); err != nil {
		// Roll back the Secret.
		_ = s.client.Delete(r.Context(), secret)
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, gitProviderSummary{
		Name:  gp.Name,
		Type:  gp.Spec.Type,
		Host:  gp.Spec.Host,
		Phase: gp.Status.Phase,
	})
}

// DeleteGitProvider deletes a GitProvider CRD and its managed webhook secret.
// Admin-only.
//
// DELETE /api/gitproviders/{name}
func (s *Server) DeleteGitProvider(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, authz.Resource{Kind: "gitprovider"}, authz.ActionDelete) {
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

	// Best-effort cleanup of the managed webhook secret.
	whSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      webhookSecretName(name),
			Namespace: git.TokenSecretNamespace,
		},
	}
	if err := s.client.Delete(r.Context(), whSecret); err != nil && !errors.IsNotFound(err) {
		writeError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetWebhookSecret returns the webhook secret value for a GitProvider so
// admins can copy it to their forge's webhook configuration.
// Admin-only.
//
// GET /api/gitproviders/{name}/webhook-secret
func (s *Server) GetWebhookSecret(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, authz.Resource{Kind: "gitprovider"}, authz.ActionUpdate) {
		return
	}

	name := chi.URLParam(r, "name")

	var gp mortisev1alpha1.GitProvider
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name}, &gp); err != nil {
		writeError(w, err)
		return
	}

	if gp.Spec.WebhookSecretRef == nil {
		writeJSON(w, http.StatusOK, map[string]string{"webhookSecret": ""})
		return
	}

	var secret corev1.Secret
	if err := s.client.Get(r.Context(), types.NamespacedName{
		Namespace: gp.Spec.WebhookSecretRef.Namespace,
		Name:      gp.Spec.WebhookSecretRef.Name,
	}, &secret); err != nil {
		writeError(w, err)
		return
	}

	v := string(secret.Data[gp.Spec.WebhookSecretRef.Key])
	writeJSON(w, http.StatusOK, map[string]string{"webhookSecret": v})
}

// webhookSecretName returns the Secret name for a GitProvider's webhook HMAC key.
func webhookSecretName(providerName string) string {
	return "gitprovider-webhook-" + providerName
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
	return ""
}
