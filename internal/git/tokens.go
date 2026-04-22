package git

import (
	"context"
	"encoding/hex"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
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

// TokenResult holds a resolved token and the email of its owner.
type TokenResult struct {
	Token string
	Email string
}

// ResolveGitTokenForApp resolves a git token with fallback across project members.
// Order: (1) cached git-token-owner annotation, (2) creator annotation,
// (3) any ProjectMember in the control namespace that has a valid token secret.
// Returns the token and the email of the user it belongs to.
func ResolveGitTokenForApp(ctx context.Context, r client.Reader, providerName, controlNamespace, createdBy, cachedOwner string) (TokenResult, error) {
	if providerName == "" {
		return TokenResult{}, fmt.Errorf("provider name is required")
	}

	// 1. Try cached token owner first (set by a previous fallback resolution).
	if cachedOwner != "" {
		token, err := ResolveGitToken(ctx, r, providerName, cachedOwner)
		if err == nil {
			return TokenResult{Token: token, Email: cachedOwner}, nil
		}
	}

	// 2. Try the app creator.
	if createdBy != "" {
		token, err := ResolveGitToken(ctx, r, providerName, createdBy)
		if err == nil {
			return TokenResult{Token: token, Email: createdBy}, nil
		}
	}

	// 3. Fall back to any project member with a valid token.
	var members mortisev1alpha1.ProjectMemberList
	if err := r.List(ctx, &members, client.InNamespace(controlNamespace)); err != nil {
		return TokenResult{}, fmt.Errorf("failed to list project members in %s: %w", controlNamespace, err)
	}

	for i := range members.Items {
		email := members.Items[i].Spec.Email
		if email == createdBy || email == cachedOwner {
			continue // already tried above
		}
		token, err := ResolveGitToken(ctx, r, providerName, email)
		if err == nil {
			return TokenResult{Token: token, Email: email}, nil
		}
	}

	return TokenResult{}, fmt.Errorf("no project member has a valid git token for provider %q", providerName)
}
