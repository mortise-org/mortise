package git

import (
	"context"
	"encoding/hex"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TokenSecretNamespace is where user git tokens and provider secrets are stored.
const TokenSecretNamespace = "mortise-system"

// UserTokenSecretName returns the k8s Secret name for a user's token for a
// specific provider. Pattern: user-{providerName}-token-{hex(email)}.
func UserTokenSecretName(providerName, email string) string {
	return fmt.Sprintf("user-%s-token-%s", providerName, hex.EncodeToString([]byte(email)))
}

// ResolveGitToken looks up a user's stored OAuth/PAT token for a given provider.
// The token is stored as a k8s Secret named user-{providerName}-token-{hex(email)}
// in the mortise-system namespace with a single key "token".
func ResolveGitToken(ctx context.Context, r client.Reader, providerName, userEmail string) (string, error) {
	if providerName == "" {
		return "", fmt.Errorf("provider name is required")
	}
	if userEmail == "" {
		return "", fmt.Errorf("user email is required for git token resolution")
	}

	secretName := UserTokenSecretName(providerName, userEmail)
	var s corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: TokenSecretNamespace,
		Name:      secretName,
	}, &s); err != nil {
		return "", fmt.Errorf("git token not found for provider %q, user %q: %w", providerName, userEmail, err)
	}

	v, ok := s.Data["token"]
	if !ok || len(v) == 0 {
		return "", fmt.Errorf("git token secret %s has no \"token\" key", secretName)
	}
	return string(v), nil
}
