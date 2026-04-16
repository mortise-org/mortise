package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"golang.org/x/oauth2"
	githuboauth "golang.org/x/oauth2/github"
	gitlaboauth "golang.org/x/oauth2/gitlab"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

const (
	// oauthStateParam is the query param name for the CSRF state token.
	oauthStateParam = "state"

	// tokenSecretNamespace is where OAuth token secrets are stored.
	// Tokens are platform-scoped, not project-scoped.
	tokenSecretNamespace = "mortise-system"
)

// OAuthHandler handles the server-side OAuth flow for git forges.
type OAuthHandler struct {
	client client.Client
}

func newOAuthHandler(c client.Client) *OAuthHandler {
	return &OAuthHandler{client: c}
}

// Authorize redirects the browser to the forge's OAuth consent URL.
//
// GET /api/oauth/{provider}/authorize
func (o *OAuthHandler) Authorize(w http.ResponseWriter, req *http.Request) {
	log := logf.FromContext(req.Context())
	providerName := chi.URLParam(req, "provider")

	cfg, err := o.oauthConfig(req.Context(), providerName, req)
	if err != nil {
		log.Error(err, "build oauth config", "provider", providerName)
		http.Error(w, "provider not configured", http.StatusBadRequest)
		return
	}

	// Use the provider name as the state token for simplicity. A production
	// implementation would use a cryptographically random, short-lived state
	// stored in a cookie or session. Deferred to a follow-up.
	state := providerName
	http.Redirect(w, req, cfg.AuthCodeURL(state, oauth2.AccessTypeOnline), http.StatusFound)
}

// Callback exchanges the authorization code for an access token and stores it
// in a k8s Secret referenced by the GitProvider.
//
// GET /api/oauth/{provider}/callback?code=...&state=...
func (o *OAuthHandler) Callback(w http.ResponseWriter, req *http.Request) {
	log := logf.FromContext(req.Context())
	providerName := chi.URLParam(req, "provider")

	code := req.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	cfg, err := o.oauthConfig(req.Context(), providerName, req)
	if err != nil {
		log.Error(err, "build oauth config", "provider", providerName)
		http.Error(w, "provider not configured", http.StatusBadRequest)
		return
	}

	tok, err := cfg.Exchange(req.Context(), code)
	if err != nil {
		log.Error(err, "exchange oauth code", "provider", providerName)
		http.Error(w, "token exchange failed", http.StatusBadGateway)
		return
	}

	if err := o.storeToken(req.Context(), providerName, tok.AccessToken); err != nil {
		log.Error(err, "store token", "provider", providerName)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, req, "/settings/git-providers?connected="+providerName, http.StatusFound)
}

// oauthConfig builds an oauth2.Config from the GitProvider CRD + resolved secrets.
func (o *OAuthHandler) oauthConfig(ctx context.Context, providerName string, req *http.Request) (*oauth2.Config, error) {
	var gp mortisev1alpha1.GitProvider
	if err := o.client.Get(ctx, types.NamespacedName{Name: providerName}, &gp); err != nil {
		return nil, fmt.Errorf("get GitProvider %q: %w", providerName, err)
	}

	clientID, err := o.resolveSecretRef(ctx, gp.Spec.OAuth.ClientIDSecretRef)
	if err != nil {
		return nil, fmt.Errorf("resolve clientID: %w", err)
	}
	clientSecret, err := o.resolveSecretRef(ctx, gp.Spec.OAuth.ClientSecretSecretRef)
	if err != nil {
		return nil, fmt.Errorf("resolve clientSecret: %w", err)
	}

	// Build callback URL from the incoming request host.
	scheme := "https"
	if req.TLS == nil && req.Header.Get("X-Forwarded-Proto") != "https" {
		scheme = "http"
	}
	callbackURL := fmt.Sprintf("%s://%s/api/oauth/%s/callback", scheme, req.Host, providerName)

	var endpoint oauth2.Endpoint
	switch gp.Spec.Type {
	case mortisev1alpha1.GitProviderTypeGitHub:
		endpoint = githuboauth.Endpoint
	case mortisev1alpha1.GitProviderTypeGitLab:
		endpoint = gitlaboauth.Endpoint
	case mortisev1alpha1.GitProviderTypeGitea:
		// Gitea OAuth2 token endpoint is at {host}/login/oauth/access_token
		endpoint = oauth2.Endpoint{
			AuthURL:  gp.Spec.Host + "/login/oauth/authorize",
			TokenURL: gp.Spec.Host + "/login/oauth/access_token",
		}
	default:
		return nil, fmt.Errorf("unsupported provider type %q", gp.Spec.Type)
	}

	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     endpoint,
		RedirectURL:  callbackURL,
		Scopes:       oauthScopes(gp.Spec.Type),
	}, nil
}

func oauthScopes(t mortisev1alpha1.GitProviderType) []string {
	switch t {
	case mortisev1alpha1.GitProviderTypeGitHub:
		return []string{"repo", "read:org"}
	case mortisev1alpha1.GitProviderTypeGitLab:
		return []string{"api", "read_user"}
	case mortisev1alpha1.GitProviderTypeGitea:
		return []string{"repository", "user"}
	default:
		return nil
	}
}

// storeToken writes the OAuth access token into a k8s Secret in mortise-system.
// The secret is named "gitprovider-token-{providerName}".
func (o *OAuthHandler) storeToken(ctx context.Context, providerName, token string) error {
	secretName := "gitprovider-token-" + providerName
	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: tokenSecretNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "mortise",
				"mortise.dev/git-provider":     providerName,
			},
		},
		Data: map[string][]byte{
			"token": []byte(token),
		},
	}

	var existing corev1.Secret
	err := o.client.Get(ctx, types.NamespacedName{Namespace: tokenSecretNamespace, Name: secretName}, &existing)
	if errors.IsNotFound(err) {
		return o.client.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("get token secret: %w", err)
	}
	// Update existing secret.
	existing.Data = desired.Data
	return o.client.Update(ctx, &existing)
}

func (o *OAuthHandler) resolveSecretRef(ctx context.Context, ref mortisev1alpha1.SecretRef) (string, error) {
	var s corev1.Secret
	if err := o.client.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &s); err != nil {
		return "", fmt.Errorf("get secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	v, ok := s.Data[ref.Key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s/%s", ref.Key, ref.Namespace, ref.Name)
	}
	return string(v), nil
}
