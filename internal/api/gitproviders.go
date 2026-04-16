package api

import (
	"context"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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
