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

// ResolveGitHubAppCredentials reads the private key and webhook secret from
// the credentials secret referenced by the GitProvider's githubApp config.
func ResolveGitHubAppCredentials(ctx context.Context, r client.Reader, gp *mortisev1alpha1.GitProvider) (privateKey []byte, webhookSecret string, err error) {
	if gp.Spec.GitHubApp == nil {
		return nil, "", fmt.Errorf("gitProvider %q has no githubApp config", gp.Name)
	}
	ref := gp.Spec.GitHubApp.CredentialsSecretRef
	var s corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &s); err != nil {
		return nil, "", fmt.Errorf("get github app credentials secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	pk, ok := s.Data["private_key"]
	if !ok || len(pk) == 0 {
		return nil, "", fmt.Errorf("credentials secret %s/%s has no \"private_key\" key", ref.Namespace, ref.Name)
	}
	ws := string(s.Data["webhook_secret"])
	return pk, ws, nil
}
