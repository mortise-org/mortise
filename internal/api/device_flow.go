package api

import (
	"context"
	"encoding/hex"
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

	// githubDeviceCodeURL is the GitHub device authorization endpoint.
	githubDeviceCodeURL = "https://github.com/login/device/code"

	// githubTokenURL is the GitHub OAuth token endpoint.
	githubTokenURL = "https://github.com/login/oauth/access_token"

	// deviceFlowScopes are the OAuth scopes requested via device flow.
	deviceFlowScopes = "repo,admin:repo_hook,read:org"
)

// DeviceFlowHandler handles the GitHub device flow for per-user GitHub
// connections.
type DeviceFlowHandler struct {
	client     client.Client
	httpClient HTTPClient
	// pending maps device_code → user email for in-flight device flows.
	// Set during RequestCode (authenticated), read during Poll (unauthenticated).
	pending map[string]string
}

// HTTPClient abstracts HTTP requests for testability.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

func newDeviceFlowHandler(c client.Client) *DeviceFlowHandler {
	return &DeviceFlowHandler{client: c, httpClient: http.DefaultClient, pending: make(map[string]string)}
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
	Status string `json:"status"`
}

// RequestCode initiates the GitHub device flow by requesting a device code.
// Requires JWT auth — the user must be logged in.
//
// POST /api/auth/github/device
func (d *DeviceFlowHandler) RequestCode(w http.ResponseWriter, r *http.Request) {
	log := logf.FromContext(r.Context())

	clientID := d.resolveClientID(r.Context())
	if clientID == defaultGitHubClientID {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{
			"GitHub not configured. An admin must set spec.github.clientID in PlatformConfig or the MORTISE_GITHUB_CLIENT_ID environment variable.",
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

	// Track which user initiated this device flow (if JWT present).
	if principal := PrincipalFromContext(r.Context()); principal != nil {
		d.pending[ghResp.DeviceCode] = principal.Email
	}

	writeJSON(w, http.StatusOK, ghResp)
}

// Poll checks whether the user has completed the device flow authorization.
// On success, stores the token. User association comes from the pending map
// (set during RequestCode when JWT was available).
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

	// Resolve user from the pending map (set during RequestCode).
	userEmail := d.pending[body.DeviceCode]
	if userEmail == "" {
		// Fallback: try JWT if present.
		if p := PrincipalFromContext(r.Context()); p != nil {
			userEmail = p.Email
		}
	}
	if userEmail == "" {
		userEmail = "default" // platform-level fallback
	}
	delete(d.pending, body.DeviceCode) // clean up

	// Store the token keyed to the user.
	if err := d.storeUserToken(r.Context(), userEmail, ghResp.AccessToken); err != nil {
		log.Error(err, "store user github token")
		http.Error(w, "failed to store credentials", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, devicePollResponse{Status: "complete"})
}

// GitHubStatus returns whether the calling user has a stored GitHub token.
//
// GET /api/auth/github/status
func (d *DeviceFlowHandler) GitHubStatus(w http.ResponseWriter, r *http.Request) {
	principal := PrincipalFromContext(r.Context())
	if principal == nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{"authentication required"})
		return
	}

	secretName := userGitHubTokenSecretName(principal.Email)
	var s corev1.Secret
	err := d.client.Get(r.Context(), types.NamespacedName{
		Namespace: tokenSecretNamespace,
		Name:      secretName,
	}, &s)

	connected := err == nil && len(s.Data["token"]) > 0
	writeJSON(w, http.StatusOK, map[string]bool{"connected": connected})
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

// userGitHubTokenSecretName returns the k8s Secret name for a user's GitHub token.
// Uses the same hex-encoded email scheme as user secrets.
func userGitHubTokenSecretName(email string) string {
	return "user-github-token-" + hex.EncodeToString([]byte(email))
}

// storeUserToken persists the GitHub access token in a k8s Secret keyed to the user.
func (d *DeviceFlowHandler) storeUserToken(ctx context.Context, email, token string) error {
	secretName := userGitHubTokenSecretName(email)
	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: tokenSecretNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "mortise",
				"mortise.dev/github-token":     "true",
				"mortise.dev/user":             hex.EncodeToString([]byte(email)),
			},
		},
		Data: map[string][]byte{
			"token": []byte(token),
		},
	}

	var existing corev1.Secret
	err := d.client.Get(ctx, types.NamespacedName{Namespace: tokenSecretNamespace, Name: secretName}, &existing)
	if errors.IsNotFound(err) {
		return d.client.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("get user token secret: %w", err)
	}
	existing.Data = desired.Data
	return d.client.Update(ctx, &existing)
}

// ResolveUserGitHubToken looks up the calling user's stored GitHub token.
// Returns the token string or an error if not found.
func ResolveUserGitHubToken(ctx context.Context, r client.Reader, email string) (string, error) {
	secretName := userGitHubTokenSecretName(email)
	var s corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: tokenSecretNamespace, Name: secretName}, &s); err != nil {
		return "", fmt.Errorf("get user github token: %w", err)
	}
	v, ok := s.Data["token"]
	if !ok || len(v) == 0 {
		return "", fmt.Errorf("user github token secret has no \"token\" key")
	}
	return string(v), nil
}
