package git

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

// tokenSecretNamespace is where OAuth token secrets are stored (matches oauth.go).
const tokenSecretNamespace = "mortise-system"

// ResolveProviderToken looks up the OAuth access token that was stored by
// internal/api/oauth.go:storeToken for the given GitProvider. The secret is
// named "gitprovider-token-{providerName}" in the mortise-system namespace and
// contains a single key "token".
func ResolveProviderToken(ctx context.Context, r client.Reader, gp *mortisev1alpha1.GitProvider) (string, error) {
	secretName := "gitprovider-token-" + gp.Name
	var s corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: tokenSecretNamespace, Name: secretName}, &s); err != nil {
		return "", fmt.Errorf("get token secret %s/%s: %w", tokenSecretNamespace, secretName, err)
	}
	v, ok := s.Data["token"]
	if !ok || len(v) == 0 {
		return "", fmt.Errorf("token secret %s/%s has no \"token\" key", tokenSecretNamespace, secretName)
	}
	return string(v), nil
}
