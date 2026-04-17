package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

const (
	// githubAppProviderName is the well-known name of the GitProvider created
	// by the GitHub App manifest flow.
	githubAppProviderName = "github-app"

	// githubAppSecretName is the name of the Secret storing GitHub App
	// credentials (private key, webhook secret, client ID, client secret).
	githubAppSecretName = "github-app-credentials"
)

// GitHubAppHandler handles the GitHub App Manifest Flow endpoints.
type GitHubAppHandler struct {
	client client.Client
}

func newGitHubAppHandler(c client.Client) *GitHubAppHandler {
	return &GitHubAppHandler{client: c}
}

// manifestRequest is the JSON body for POST /api/github-app/manifest.
// No fields are required — domain is read from PlatformConfig.
type manifestRequest struct{}

// manifestResponse is returned to the frontend so it can POST the form to GitHub.
type manifestResponse struct {
	RedirectURL string         `json:"redirectUrl"`
	Manifest    map[string]any `json:"manifest"`
	State       string         `json:"state"`
}

// GenerateManifest generates the GitHub App manifest and returns the redirect
// URL. The frontend creates a hidden form and POSTs the manifest to GitHub.
//
// POST /api/github-app/manifest
func (h *GitHubAppHandler) GenerateManifest(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	log := logf.FromContext(r.Context())

	domain, err := h.platformDomain(r.Context())
	if err != nil {
		log.Error(err, "read platform domain")
		writeJSON(w, http.StatusBadRequest, errorResponse{"platform domain not configured — set it in Settings > Platform first"})
		return
	}

	state, err := generateOAuthState()
	if err != nil {
		log.Error(err, "generate state")
		writeJSON(w, http.StatusInternalServerError, errorResponse{"internal error"})
		return
	}

	manifest := map[string]any{
		"name": fmt.Sprintf("Mortise (%s)", domain),
		"url":  "https://" + domain,
		"hook_attributes": map[string]any{
			"url":    "https://" + domain + "/api/webhooks/github",
			"active": true,
		},
		"redirect_url":  "https://" + domain + "/api/github-app/callback",
		"callback_urls": []string{"https://" + domain + "/api/github-app/callback"},
		"setup_url":     "https://" + domain + "/settings/git-providers?github-app-installed=true",
		"public":        false,
		"default_permissions": map[string]string{
			"contents":       "read",
			"metadata":       "read",
			"pull_requests":  "write",
			"statuses":       "write",
			"administration": "read",
		},
		"default_events": []string{"push", "pull_request"},
	}

	writeJSON(w, http.StatusOK, manifestResponse{
		RedirectURL: "https://github.com/settings/apps/new?state=" + state,
		Manifest:    manifest,
		State:       state,
	})
}

// Callback handles the redirect from GitHub after the user creates the app.
// It exchanges the temporary code for full credentials and stores them.
//
// GET /api/github-app/callback?code={code}
func (h *GitHubAppHandler) Callback(w http.ResponseWriter, r *http.Request) {
	log := logf.FromContext(r.Context())

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code parameter", http.StatusBadRequest)
		return
	}

	// Exchange the code with GitHub.
	appData, err := exchangeManifestCode(code)
	if err != nil {
		log.Error(err, "exchange manifest code")
		http.Error(w, "failed to exchange code with GitHub: "+err.Error(), http.StatusBadGateway)
		return
	}

	ctx := r.Context()

	// Store credentials in a Secret.
	if err := h.storeCredentials(ctx, appData); err != nil {
		log.Error(err, "store github app credentials")
		http.Error(w, "failed to store credentials", http.StatusInternalServerError)
		return
	}

	// Create or update the GitProvider CRD.
	if err := h.ensureGitProvider(ctx, appData); err != nil {
		log.Error(err, "create github app git provider")
		http.Error(w, "failed to create git provider", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/settings/git-providers?github-app-created=true", http.StatusFound)
}

// githubAppData holds the fields returned from the manifest code exchange.
type githubAppData struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	PEM           string `json:"pem"`
	WebhookSecret string `json:"webhook_secret"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	HTMLURL       string `json:"html_url"`
}

// exchangeManifestCode calls GitHub's manifest conversion endpoint.
func exchangeManifestCode(code string) (*githubAppData, error) {
	url := "https://api.github.com/app-manifests/" + code + "/conversions"
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("github returned %d: %s", resp.StatusCode, string(body))
	}

	var data githubAppData
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &data, nil
}

func (h *GitHubAppHandler) storeCredentials(ctx context.Context, data *githubAppData) error {
	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      githubAppSecretName,
			Namespace: tokenSecretNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "mortise",
				"mortise.dev/managed-by":       "api",
				"mortise.dev/git-provider":     githubAppProviderName,
			},
		},
		Data: map[string][]byte{
			"app_id":         []byte(strconv.FormatInt(data.ID, 10)),
			"private_key":    []byte(data.PEM),
			"webhook_secret": []byte(data.WebhookSecret),
			"client_id":      []byte(data.ClientID),
			"client_secret":  []byte(data.ClientSecret),
		},
	}

	var existing corev1.Secret
	err := h.client.Get(ctx, types.NamespacedName{Namespace: tokenSecretNamespace, Name: githubAppSecretName}, &existing)
	if errors.IsNotFound(err) {
		return h.client.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("get existing secret: %w", err)
	}
	existing.Data = desired.Data
	return h.client.Update(ctx, &existing)
}

func (h *GitHubAppHandler) ensureGitProvider(ctx context.Context, data *githubAppData) error {
	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: githubAppProviderName},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitHub,
			Host: "https://github.com",
			Mode: "github-app",
			GitHubApp: &mortisev1alpha1.GitHubAppConfig{
				AppID: data.ID,
				Slug:  data.Slug,
				CredentialsSecretRef: mortisev1alpha1.SecretRef{
					Namespace: tokenSecretNamespace,
					Name:      githubAppSecretName,
					Key:       "private_key",
				},
			},
			WebhookSecretRef: mortisev1alpha1.SecretRef{
				Namespace: tokenSecretNamespace,
				Name:      githubAppSecretName,
				Key:       "webhook_secret",
			},
		},
	}

	var existing mortisev1alpha1.GitProvider
	err := h.client.Get(ctx, types.NamespacedName{Name: githubAppProviderName}, &existing)
	if errors.IsNotFound(err) {
		if err := h.client.Create(ctx, gp); err != nil {
			return err
		}
		// Set status to Ready.
		gp.Status.Phase = mortisev1alpha1.GitProviderPhaseReady
		return h.client.Status().Update(ctx, gp)
	}
	if err != nil {
		return fmt.Errorf("get existing git provider: %w", err)
	}

	existing.Spec = gp.Spec
	if err := h.client.Update(ctx, &existing); err != nil {
		return err
	}
	existing.Status.Phase = mortisev1alpha1.GitProviderPhaseReady
	return h.client.Status().Update(ctx, &existing)
}

func (h *GitHubAppHandler) platformDomain(ctx context.Context) (string, error) {
	var pc mortisev1alpha1.PlatformConfig
	if err := h.client.Get(ctx, types.NamespacedName{Name: platformConfigName}, &pc); err != nil {
		return "", fmt.Errorf("get PlatformConfig: %w", err)
	}
	if pc.Spec.Domain == "" {
		return "", fmt.Errorf("PlatformConfig domain is empty")
	}
	return pc.Spec.Domain, nil
}
