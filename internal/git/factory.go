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

// NewGitHubAppAPIFromProvider constructs a GitHubAppAPI from a GitProvider CRD
// with mode=github-app. privateKeyPEM and webhookSecret are read from the
// credentials secret referenced by spec.githubApp.credentialsSecretRef.
func NewGitHubAppAPIFromProvider(gp *mortisev1alpha1.GitProvider, privateKeyPEM []byte, webhookSecret string) (GitAPI, error) {
	if gp.Spec.GitHubApp == nil {
		return nil, fmt.Errorf("gitProvider %q has no githubApp config", gp.Name)
	}
	api, err := NewGitHubAppAPI(gp.Spec.Host, gp.Spec.GitHubApp.AppID, privateKeyPEM, webhookSecret)
	if err != nil {
		return nil, err
	}
	if gp.Spec.GitHubApp.InstallationID != 0 {
		api.SetInstallationID(gp.Spec.GitHubApp.InstallationID)
	}
	return api, nil
}
