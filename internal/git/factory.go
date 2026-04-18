package git

import (
	"fmt"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

// NewGitAPIFromProvider constructs the correct GitAPI implementation from a
// GitProvider CRD and a resolved OAuth access token and webhook secret.
// token is the user's OAuth access token, webhookSecret is the HMAC key
// stored in spec.webhookSecretRef.
func NewGitAPIFromProvider(gp *mortisev1alpha1.GitProvider, token, webhookSecret string) (GitAPI, error) {
	switch gp.Spec.Type {
	case mortisev1alpha1.GitProviderTypeGitHub:
		return NewGitHubAPI(gp.Spec.Host, token, webhookSecret)
	case mortisev1alpha1.GitProviderTypeGitLab:
		return NewGitLabAPI(gp.Spec.Host, token, webhookSecret)
	case mortisev1alpha1.GitProviderTypeGitea:
		return NewGiteaAPI(gp.Spec.Host, token, webhookSecret)
	default:
		return nil, fmt.Errorf("unsupported git provider type: %q", gp.Spec.Type)
	}
}
