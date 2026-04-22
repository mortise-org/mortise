package api

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/git"
)

const (
	// deviceFlowScopes are the OAuth scopes requested via device flow.
	deviceFlowScopes = "repo,admin:repo_hook,read:org"
)

// DeviceFlowHandler handles the OAuth device flow for per-user git provider
// connections. Currently supports GitHub; generalizable to any provider that
// implements the device authorization grant (RFC 8628).
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

// deviceCodeResponse is the JSON body returned from the device code request.
type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// devicePollRequest is the JSON body for the poll endpoint.
type devicePollRequest struct {
	DeviceCode string `json:"device_code"`
}

// devicePollResponse is the JSON body returned from the poll endpoint.
type devicePollResponse struct {
	Status string `json:"status"`
}

// deviceCodeEndpoint returns the OAuth device code endpoint for the given host.
// For github.com: https://github.com/login/device/code
// For GHE: https://{host}/login/device/code
func deviceCodeEndpoint(host string) string {
	return strings.TrimRight(host, "/") + "/login/device/code"
}

// tokenEndpoint returns the OAuth token endpoint for the given host.
func tokenEndpoint(host string) string {
	return strings.TrimRight(host, "/") + "/login/oauth/access_token"
}

// RequestCode initiates the device flow by requesting a device code from the
// git forge. Requires JWT auth — the user must be logged in.
//
// POST /api/auth/git/{provider}/device
func (d *DeviceFlowHandler) RequestCode(w http.ResponseWriter, r *http.Request) {
	log := logf.FromContext(r.Context())
	providerName := chi.URLParam(r, "provider")
	providerType, defaultHost := inferProviderType(providerName)

	gp, err := d.getOrCreateGitProvider(r.Context(), providerName, providerType, defaultHost)
	if err != nil {
		log.Error(err, "get git provider", "provider", providerName)
		writeJSON(w, http.StatusNotFound, errorResponse{err.Error()})
		return
	}

	if gp.Spec.ClientID == "" {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{
			fmt.Sprintf("Git provider %q has no clientID configured.", providerName),
		})
		return
	}

	form := url.Values{
		"client_id": {gp.Spec.ClientID},
		"scope":     {deviceFlowScopes},
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		deviceCodeEndpoint(gp.Spec.Host), strings.NewReader(form.Encode()))
	if err != nil {
		log.Error(err, "build device code request")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		log.Error(err, "request device code", "provider", providerName)
		http.Error(w, "failed to contact git provider", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Info("device code request failed", "provider", providerName, "status", resp.StatusCode)
		http.Error(w, "git provider returned an error", http.StatusBadGateway)
		return
	}

	var codeResp deviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&codeResp); err != nil {
		log.Error(err, "decode device code response")
		http.Error(w, "failed to parse provider response", http.StatusBadGateway)
		return
	}

	writeJSON(w, http.StatusOK, codeResp)
}

// Poll checks whether the user has completed the device flow authorization.
// On success, stores the token keyed to the authenticated user and provider.
// Requires JWT auth — user identity comes from the JWT, not an in-memory map.
//
// POST /api/auth/git/{provider}/device/poll
func (d *DeviceFlowHandler) Poll(w http.ResponseWriter, r *http.Request) {
	log := logf.FromContext(r.Context())
	providerName := chi.URLParam(r, "provider")

	principal := PrincipalFromContext(r.Context())
	if principal == nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{"authentication required"})
		return
	}

	var body devicePollRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if body.DeviceCode == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"device_code is required"})
		return
	}

	providerType, defaultHost := inferProviderType(providerName)
	gp, err := d.getOrCreateGitProvider(r.Context(), providerName, providerType, defaultHost)
	if err != nil {
		log.Error(err, "get git provider", "provider", providerName)
		writeJSON(w, http.StatusNotFound, errorResponse{"git provider not found: " + providerName})
		return
	}

	form := url.Values{
		"client_id":   {gp.Spec.ClientID},
		"device_code": {body.DeviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		tokenEndpoint(gp.Spec.Host), strings.NewReader(form.Encode()))
	if err != nil {
		log.Error(err, "build token poll request")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		log.Error(err, "poll token endpoint", "provider", providerName)
		http.Error(w, "failed to contact git provider", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		log.Error(err, "decode token poll response")
		http.Error(w, "failed to parse provider response", http.StatusBadGateway)
		return
	}

	switch tokenResp.Error {
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
		log.Info("token poll error", "provider", providerName, "error", tokenResp.Error)
		writeJSON(w, http.StatusOK, devicePollResponse{Status: "error"})
		return
	}

	if tokenResp.AccessToken == "" {
		writeJSON(w, http.StatusOK, devicePollResponse{Status: "pending"})
		return
	}

	if err := d.storeUserToken(r.Context(), providerName, principal.Email, tokenResp.AccessToken); err != nil {
		log.Error(err, "store user git token", "provider", providerName)
		http.Error(w, "failed to store credentials", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, devicePollResponse{Status: "complete"})
}

// GitTokenStatus returns whether the calling user has a stored token for the
// given git provider.
//
// GET /api/auth/git/{provider}/status
func (d *DeviceFlowHandler) GitTokenStatus(w http.ResponseWriter, r *http.Request) {
	providerName := chi.URLParam(r, "provider")

	principal := PrincipalFromContext(r.Context())
	if principal == nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{"authentication required"})
		return
	}

	secretName := git.UserTokenSecretName(providerName, principal.Email)
	var s corev1.Secret
	err := d.client.Get(r.Context(), types.NamespacedName{
		Namespace: git.TokenSecretNamespace,
		Name:      secretName,
	}, &s)

	connected := err == nil && len(s.Data["token"]) > 0
	writeJSON(w, http.StatusOK, map[string]bool{"connected": connected})
}

// storePATRequest is the JSON body for the PAT store endpoint.
type storePATRequest struct {
	Token string `json:"token"`
	Host  string `json:"host,omitempty"`
}

// StorePAT stores a personal access token for the given provider, keyed to the
// authenticated user. Creates the GitProvider CRD if it doesn't exist yet.
//
// POST /api/auth/git/{provider}/token
func (d *DeviceFlowHandler) StorePAT(w http.ResponseWriter, r *http.Request) {
	providerName := chi.URLParam(r, "provider")

	principal := PrincipalFromContext(r.Context())
	if principal == nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{"authentication required"})
		return
	}

	var body storePATRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if body.Token == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"token is required"})
		return
	}

	providerType, defaultHost := inferProviderType(providerName)
	host := body.Host
	if host == "" {
		host = defaultHost
	}

	if _, err := d.getOrCreateGitProvider(r.Context(), providerName, providerType, host); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{"git provider not found: " + providerName})
		return
	}

	if err := d.storeUserToken(r.Context(), providerName, principal.Email, body.Token); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"failed to store token: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// providerDefaults maps provider types to their default hosts and env var names
// for client ID lookup.
var providerDefaults = map[mortisev1alpha1.GitProviderType]struct {
	host        string
	clientIDEnv string
}{
	mortisev1alpha1.GitProviderTypeGitHub: {host: "https://github.com", clientIDEnv: "MORTISE_GITHUB_CLIENT_ID"},
	mortisev1alpha1.GitProviderTypeGitLab: {host: "https://gitlab.com", clientIDEnv: "MORTISE_GITLAB_CLIENT_ID"},
	mortisev1alpha1.GitProviderTypeGitea:  {host: "https://gitea.com", clientIDEnv: "MORTISE_GITEA_CLIENT_ID"},
}

// inferProviderType maps a URL-parameter provider name to the corresponding
// CRD type and default host. Falls back to GitHub for unrecognized names.
func inferProviderType(name string) (mortisev1alpha1.GitProviderType, string) {
	switch {
	case strings.HasPrefix(name, "gitlab"):
		return mortisev1alpha1.GitProviderTypeGitLab, providerDefaults[mortisev1alpha1.GitProviderTypeGitLab].host
	case strings.HasPrefix(name, "gitea"):
		return mortisev1alpha1.GitProviderTypeGitea, providerDefaults[mortisev1alpha1.GitProviderTypeGitea].host
	default:
		return mortisev1alpha1.GitProviderTypeGitHub, providerDefaults[mortisev1alpha1.GitProviderTypeGitHub].host
	}
}

// getOrCreateGitProvider looks up the GitProvider CRD by name. If it doesn't
// exist and a default client ID is available (e.g. from MORTISE_GITHUB_CLIENT_ID,
// MORTISE_GITLAB_CLIENT_ID, or MORTISE_GITEA_CLIENT_ID), it creates the provider
// on-demand. This eliminates the race between PlatformConfig reconciliation and
// the user clicking "Connect."
func (d *DeviceFlowHandler) getOrCreateGitProvider(ctx context.Context, name string, providerType mortisev1alpha1.GitProviderType, host string) (*mortisev1alpha1.GitProvider, error) {
	var gp mortisev1alpha1.GitProvider
	err := d.client.Get(ctx, types.NamespacedName{Name: name}, &gp)
	if err == nil {
		return &gp, nil
	}
	if !k8serrors.IsNotFound(err) {
		return nil, fmt.Errorf("get git provider %q: %w", name, err)
	}

	// Provider doesn't exist — try to create with the appropriate type.
	defaults, ok := providerDefaults[providerType]
	if !ok {
		return nil, fmt.Errorf("git provider %q not found and unsupported provider type %q", name, providerType)
	}
	clientID := os.Getenv(defaults.clientIDEnv)
	if clientID == "" {
		return nil, fmt.Errorf("git provider %q not found and no default client ID available (set %s)", name, defaults.clientIDEnv)
	}

	// Auto-generate webhook secret.
	webhookSecretBytes := make([]byte, 32)
	if _, err := cryptorand.Read(webhookSecretBytes); err != nil {
		return nil, fmt.Errorf("generate webhook secret: %w", err)
	}
	secretName := "gitprovider-webhook-" + name
	whSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: git.TokenSecretNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "mortise",
				"mortise.dev/git-provider":     name,
			},
		},
		Data: map[string][]byte{
			"webhookSecret": []byte(hex.EncodeToString(webhookSecretBytes)),
		},
	}
	if err := d.client.Create(ctx, whSecret); err != nil && !k8serrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("create webhook secret: %w", err)
	}

	newGP := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type:     providerType,
			Host:     host,
			ClientID: clientID,
			WebhookSecretRef: &mortisev1alpha1.SecretRef{
				Namespace: git.TokenSecretNamespace,
				Name:      secretName,
				Key:       "webhookSecret",
			},
		},
	}
	if err := d.client.Create(ctx, newGP); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			// Race: another request created it first. Read and return.
			if err := d.client.Get(ctx, types.NamespacedName{Name: name}, &gp); err != nil {
				return nil, err
			}
			return &gp, nil
		}
		return nil, fmt.Errorf("create git provider %q: %w", name, err)
	}
	return newGP, nil
}

// storeUserToken persists a git provider access token in a k8s Secret keyed
// to the user and provider. Uses the generic naming convention from git.UserTokenSecretName.
func (d *DeviceFlowHandler) storeUserToken(ctx context.Context, providerName, email, token string) error {
	secretName := git.UserTokenSecretName(providerName, email)
	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: git.TokenSecretNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "mortise",
				"mortise.dev/git-token":        "true",
				"mortise.dev/provider":         providerName,
				"mortise.dev/user":             hex.EncodeToString([]byte(email)),
			},
		},
		Data: map[string][]byte{
			"token": []byte(token),
		},
	}

	var existing corev1.Secret
	err := d.client.Get(ctx, types.NamespacedName{
		Namespace: git.TokenSecretNamespace,
		Name:      secretName,
	}, &existing)
	if k8serrors.IsNotFound(err) {
		return d.client.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("get user token secret: %w", err)
	}
	existing.Data = desired.Data
	existing.Labels = desired.Labels
	return d.client.Update(ctx, &existing)
}
