package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

const (
	// defaultGitHubClientID is the Mortise-project-maintained GitHub OAuth App
	// client ID. Replaced at release time with the real value.
	defaultGitHubClientID = "MORTISE_GITHUB_CLIENT_ID_PLACEHOLDER"

	// deviceFlowProviderName is the GitProvider name auto-created by the device flow.
	deviceFlowProviderName = "github"

	// githubDeviceCodeURL is the GitHub device authorization endpoint.
	githubDeviceCodeURL = "https://github.com/login/device/code"

	// githubTokenURL is the GitHub OAuth token endpoint.
	githubTokenURL = "https://github.com/login/oauth/access_token"

	// deviceFlowScopes are the OAuth scopes requested via device flow.
	deviceFlowScopes = "repo,admin:repo_hook"
)

// DeviceFlowHandler handles the GitHub device flow for zero-config GitHub
// connections.
type DeviceFlowHandler struct {
	client     client.Client
	httpClient HTTPClient
}

// HTTPClient abstracts HTTP requests for testability.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

func newDeviceFlowHandler(c client.Client) *DeviceFlowHandler {
	return &DeviceFlowHandler{client: c, httpClient: http.DefaultClient}
}

// deviceCodeResponse is the JSON body returned from POST /api/auth/github/device.
type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// devicePollRequest is the JSON body for POST /api/auth/github/device/poll.
type devicePollRequest struct {
	DeviceCode string `json:"device_code"`
}

// devicePollResponse is the JSON body returned from the poll endpoint.
type devicePollResponse struct {
	Status      string `json:"status"`
	AccessToken string `json:"access_token,omitempty"`
}

// RequestCode initiates the GitHub device flow by requesting a device code.
//
// POST /api/auth/github/device
func (d *DeviceFlowHandler) RequestCode(w http.ResponseWriter, r *http.Request) {
	log := logf.FromContext(r.Context())

	clientID := d.resolveClientID(r.Context())
	if clientID == defaultGitHubClientID {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{
			"GitHub App not configured. An admin must set spec.github.clientID in PlatformConfig or the MORTISE_GITHUB_CLIENT_ID environment variable.",
		})
		return
	}

	form := url.Values{
		"client_id": {clientID},
		"scope":     {deviceFlowScopes},
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, githubDeviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		log.Error(err, "build device code request")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		log.Error(err, "request device code from GitHub")
		http.Error(w, "failed to contact GitHub", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Info("GitHub device code request failed", "status", resp.StatusCode)
		http.Error(w, "GitHub returned an error", http.StatusBadGateway)
		return
	}

	var ghResp deviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&ghResp); err != nil {
		log.Error(err, "decode device code response")
		http.Error(w, "failed to parse GitHub response", http.StatusBadGateway)
		return
	}

	writeJSON(w, http.StatusOK, ghResp)
}

// Poll checks whether the user has completed the device flow authorization.
//
// POST /api/auth/github/device/poll
func (d *DeviceFlowHandler) Poll(w http.ResponseWriter, r *http.Request) {
	log := logf.FromContext(r.Context())

	var body devicePollRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if body.DeviceCode == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"device_code is required"})
		return
	}

	clientID := d.resolveClientID(r.Context())

	form := url.Values{
		"client_id":   {clientID},
		"device_code": {body.DeviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, githubTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		log.Error(err, "build token poll request")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		log.Error(err, "poll GitHub token endpoint")
		http.Error(w, "failed to contact GitHub", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	var ghResp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ghResp); err != nil {
		log.Error(err, "decode token poll response")
		http.Error(w, "failed to parse GitHub response", http.StatusBadGateway)
		return
	}

	switch ghResp.Error {
	case "authorization_pending":
		writeJSON(w, http.StatusOK, devicePollResponse{Status: "pending"})
		return
	case "slow_down":
		writeJSON(w, http.StatusOK, devicePollResponse{Status: "slow_down"})
		return
	case "expired_token":
		writeJSON(w, http.StatusOK, devicePollResponse{Status: "expired"})
		return
	case "access_denied":
		writeJSON(w, http.StatusOK, devicePollResponse{Status: "denied"})
		return
	case "":
		// Success — fall through.
	default:
		log.Info("GitHub token poll error", "error", ghResp.Error)
		writeJSON(w, http.StatusOK, devicePollResponse{Status: "error"})
		return
	}

	if ghResp.AccessToken == "" {
		writeJSON(w, http.StatusOK, devicePollResponse{Status: "pending"})
		return
	}

	// Store the token and auto-create the GitProvider.
	if err := d.storeTokenAndCreateProvider(r.Context(), ghResp.AccessToken); err != nil {
		log.Error(err, "store device flow token")
		http.Error(w, "failed to store credentials", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, devicePollResponse{
		Status:      "complete",
		AccessToken: ghResp.AccessToken,
	})
}

// resolveClientID reads the GitHub client ID from: PlatformConfig -> env var -> default constant.
func (d *DeviceFlowHandler) resolveClientID(ctx context.Context) string {
	// 1. PlatformConfig override.
	var pc mortisev1alpha1.PlatformConfig
	if err := d.client.Get(ctx, types.NamespacedName{Name: platformConfigName}, &pc); err == nil {
		if pc.Spec.GitHub != nil && pc.Spec.GitHub.ClientID != "" {
			return pc.Spec.GitHub.ClientID
		}
	}

	// 2. Environment variable.
	if v := os.Getenv("MORTISE_GITHUB_CLIENT_ID"); v != "" {
		return v
	}

	// 3. Built-in default.
	return defaultGitHubClientID
}

// storeTokenAndCreateProvider persists the access token in a k8s Secret and
// creates or updates the GitProvider CRD for the default device-flow connection.
func (d *DeviceFlowHandler) storeTokenAndCreateProvider(ctx context.Context, token string) error {
	// Store the token secret (same pattern as OAuthHandler.storeToken).
	secretName := "gitprovider-token-" + deviceFlowProviderName
	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: tokenSecretNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "mortise",
				"mortise.dev/git-provider":     deviceFlowProviderName,
			},
		},
		Data: map[string][]byte{
			"token": []byte(token),
		},
	}

	var existing corev1.Secret
	err := d.client.Get(ctx, types.NamespacedName{Namespace: tokenSecretNamespace, Name: secretName}, &existing)
	if errors.IsNotFound(err) {
		if err := d.client.Create(ctx, desired); err != nil {
			return fmt.Errorf("create token secret: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("get token secret: %w", err)
	} else {
		existing.Data = desired.Data
		if err := d.client.Update(ctx, &existing); err != nil {
			return fmt.Errorf("update token secret: %w", err)
		}
	}

	// Create or update the GitProvider CRD.
	var gp mortisev1alpha1.GitProvider
	err = d.client.Get(ctx, types.NamespacedName{Name: deviceFlowProviderName}, &gp)
	if errors.IsNotFound(err) {
		gp = mortisev1alpha1.GitProvider{
			ObjectMeta: metav1.ObjectMeta{Name: deviceFlowProviderName},
			Spec: mortisev1alpha1.GitProviderSpec{
				Type: mortisev1alpha1.GitProviderTypeGitHub,
				Host: "https://github.com",
				// Device flow is a public client — no OAuth secret needed.
				// Provide empty refs so the CRD validates.
				OAuth: mortisev1alpha1.OAuthConfig{
					ClientIDSecretRef:     mortisev1alpha1.SecretRef{Namespace: tokenSecretNamespace, Name: secretName, Key: "token"},
					ClientSecretSecretRef: mortisev1alpha1.SecretRef{Namespace: tokenSecretNamespace, Name: secretName, Key: "token"},
				},
				WebhookSecretRef: mortisev1alpha1.SecretRef{Namespace: tokenSecretNamespace, Name: secretName, Key: "token"},
			},
		}
		if err := d.client.Create(ctx, &gp); err != nil {
			return fmt.Errorf("create GitProvider: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("get GitProvider: %w", err)
	}

	// Set status to Ready.
	gp.Status.Phase = mortisev1alpha1.GitProviderPhaseReady
	if err := d.client.Status().Update(ctx, &gp); err != nil {
		return fmt.Errorf("update GitProvider status: %w", err)
	}

	return nil
}
