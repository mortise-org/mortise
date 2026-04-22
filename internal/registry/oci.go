package registry

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config holds the connection parameters for a generic OCI-compliant registry.
// Suitable for Zot, Harbor, Docker Registry v2, GHCR, ECR — anything that
// implements the OCI Distribution Spec v1.1.
type Config struct {
	// URL is the registry base URL, e.g. "https://registry.example.com" or
	// "http://localhost:5000". Required.
	URL string

	// Namespace prefixes every image path pushed by the operator.
	// Defaults to "mortise" if empty. Spec §7.5: images are stored at
	// <registry>/<namespace>/<app-name>:<tag>.
	Namespace string

	// Username and Password are used for HTTP Basic auth.
	// Ignored if BearerToken is set.
	Username string
	Password string

	// BearerToken is a pre-issued token used as "Authorization: Bearer <token>".
	// When set, Username/Password are ignored for initial requests but are still
	// forwarded during Www-Authenticate challenge resolution.
	BearerToken string

	// PullSecretName is the name of the k8s Secret (in the app's namespace) that
	// contains Docker config JSON for pulling images from this registry. The
	// operator is responsible for creating this Secret; OCIBackend only surfaces
	// the name so that the controller can reference it in Pod specs.
	PullSecretName string

	// InsecureSkipTLSVerify disables TLS certificate verification. Use only for
	// local k3d dev clusters where the registry has a self-signed cert.
	InsecureSkipTLSVerify bool

	// PullURL is the registry URL that kubelet uses to pull images. When the
	// bundled registry runs behind a node-local proxy, this differs from URL.
	// If empty, URL is used for both push and pull.
	PullURL string
}

// OCIBackend implements RegistryBackend against any OCI Distribution Spec v1.1
// compliant registry. Zero Zot-specific code; no Docker CLI or moby deps.
type OCIBackend struct {
	cfg    Config
	client *http.Client
}

// NewOCIBackend constructs an OCIBackend from cfg. If cfg.Namespace is empty,
// "mortise" is used. The caller is responsible for ensuring cfg.URL is reachable.
func NewOCIBackend(cfg Config) *OCIBackend {
	if cfg.Namespace == "" {
		cfg.Namespace = "mortise"
	}

	transport := http.DefaultTransport
	if cfg.InsecureSkipTLSVerify {
		transport = insecureTransport()
	}

	return &OCIBackend{
		cfg: cfg,
		client: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}
}

// imagePath returns the registry path for the given app name.
// e.g. "mortise/my-app"
func (b *OCIBackend) imagePath(app string) string {
	return b.cfg.Namespace + "/" + app
}

// PushTarget returns the ImageRef the build system should push to.
// This is a pure computation — no network call. The operator passes the
// returned ImageRef.Full to the BuildClient as the push destination.
func (b *OCIBackend) PushTarget(app, tag string) (ImageRef, error) {
	if app == "" {
		return ImageRef{}, fmt.Errorf("app name must not be empty")
	}
	if tag == "" {
		return ImageRef{}, fmt.Errorf("tag must not be empty")
	}

	base, err := registryHost(b.cfg.URL)
	if err != nil {
		return ImageRef{}, fmt.Errorf("invalid registry URL %q: %w", b.cfg.URL, err)
	}

	path := b.imagePath(app)
	return ImageRef{
		Registry: base,
		Path:     path,
		Tag:      tag,
		Full:     base + "/" + path + ":" + tag,
	}, nil
}

// PullTarget returns the ImageRef that kubelet should pull from. When PullURL
// is configured (e.g. a node-local DaemonSet proxy at localhost:30500), the
// returned ref uses that host instead of the push registry. Falls back to
// PushTarget when PullURL is empty.
func (b *OCIBackend) PullTarget(app, tag string) (ImageRef, error) {
	if b.cfg.PullURL == "" {
		return b.PushTarget(app, tag)
	}

	if app == "" {
		return ImageRef{}, fmt.Errorf("app name must not be empty")
	}
	if tag == "" {
		return ImageRef{}, fmt.Errorf("tag must not be empty")
	}

	base, err := registryHost(b.cfg.PullURL)
	if err != nil {
		return ImageRef{}, fmt.Errorf("invalid pull URL %q: %w", b.cfg.PullURL, err)
	}

	path := b.imagePath(app)
	return ImageRef{
		Registry: base,
		Path:     path,
		Tag:      tag,
		Full:     base + "/" + path + ":" + tag,
	}, nil
}

// PullSecretRef returns the name of the k8s Secret that holds Docker config
// JSON credentials for this registry. Controllers reference this name when
// building Pod specs so Kubernetes can pull images.
func (b *OCIBackend) PullSecretRef() string {
	return b.cfg.PullSecretName
}

// Tags lists all tags for the given app's image repository.
// Calls GET /v2/<namespace>/<app>/tags/list per OCI Distribution Spec §10.3.
func (b *OCIBackend) Tags(ctx context.Context, app string) ([]string, error) {
	path := b.imagePath(app)
	endpoint := b.cfg.URL + "/v2/" + path + "/tags/list"

	resp, err := b.do(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("listing tags for %s: %w", app, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// Repository does not exist yet — return empty list, not an error.
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tags/list returned %d for %s", resp.StatusCode, app)
	}

	var payload struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decoding tags response: %w", err)
	}
	if payload.Tags == nil {
		return []string{}, nil
	}
	return payload.Tags, nil
}

// DeleteTag deletes the manifest identified by tag from the registry.
// OCI Distribution Spec §10.4 requires deleting by digest, so DeleteTag first
// resolves the digest via a HEAD on the manifests endpoint, then issues
// DELETE /v2/<namespace>/<app>/manifests/<digest>.
func (b *OCIBackend) DeleteTag(ctx context.Context, app, tag string) error {
	path := b.imagePath(app)

	// Step 1: resolve digest.
	headURL := b.cfg.URL + "/v2/" + path + "/manifests/" + tag
	resp, err := b.do(ctx, http.MethodHead, headURL, nil)
	if err != nil {
		return fmt.Errorf("resolving digest for %s:%s: %w", app, tag, err)
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("tag %s not found for app %s", tag, app)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HEAD manifests returned %d for %s:%s", resp.StatusCode, app, tag)
	}

	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		// Fall back to Content-Digest (OCI spec variant).
		digest = resp.Header.Get("Content-Digest")
	}
	if digest == "" {
		return fmt.Errorf("registry did not return a digest header for %s:%s", app, tag)
	}

	// Step 2: delete by digest.
	deleteURL := b.cfg.URL + "/v2/" + path + "/manifests/" + digest
	delResp, err := b.do(ctx, http.MethodDelete, deleteURL, nil)
	if err != nil {
		return fmt.Errorf("deleting manifest for %s:%s: %w", app, tag, err)
	}
	delResp.Body.Close()

	if delResp.StatusCode == http.StatusAccepted || delResp.StatusCode == http.StatusOK {
		return nil
	}
	return fmt.Errorf("DELETE manifests returned %d for %s:%s", delResp.StatusCode, app, tag)
}

// do executes an HTTP request, applying auth. On a 401 with a Www-Authenticate
// challenge it negotiates a bearer token and retries once — this is the standard
// OCI Distribution Spec auth flow (§10.2 / Docker Token Auth spec).
func (b *OCIBackend) do(ctx context.Context, method, rawURL string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	b.applyStaticAuth(req)

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	// Consume and close the 401 body before retrying.
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	// Negotiate a token from the challenge and retry.
	token, err := b.resolveChallenge(ctx, resp.Header.Get("Www-Authenticate"))
	if err != nil {
		return nil, fmt.Errorf("auth challenge: %w", err)
	}

	req2, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req2.Header.Set("Authorization", "Bearer "+token)
	return b.client.Do(req2)
}

// applyStaticAuth attaches the configured static credentials to req.
func (b *OCIBackend) applyStaticAuth(req *http.Request) {
	if b.cfg.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+b.cfg.BearerToken)
		return
	}
	if b.cfg.Username != "" {
		req.SetBasicAuth(b.cfg.Username, b.cfg.Password)
	}
}

// resolveChallenge parses a Www-Authenticate header and obtains a bearer token.
//
// The OCI/Docker Token Auth spec defines:
//
//	Www-Authenticate: Bearer realm="<url>",service="<service>",scope="<scope>"
//
// We fetch GET <realm>?service=<service>&scope=<scope> with Basic auth if
// credentials are configured and return the token from the JSON response.
// If the header describes Basic auth (not Bearer), we return the empty string
// so the caller falls back to Basic auth on retry.
func (b *OCIBackend) resolveChallenge(ctx context.Context, header string) (string, error) {
	if header == "" {
		return "", fmt.Errorf("empty Www-Authenticate header")
	}

	scheme, params, err := parseWWWAuthenticate(header)
	if err != nil {
		return "", err
	}

	if !strings.EqualFold(scheme, "Bearer") {
		// Basic auth challenge — the caller must switch to Basic, but we have no
		// retry mechanism for Basic here (applyStaticAuth already handled it).
		return "", fmt.Errorf("unsupported auth scheme %q", scheme)
	}

	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("Bearer challenge missing realm")
	}

	tokenURL, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("invalid realm URL %q: %w", realm, err)
	}
	q := tokenURL.Query()
	if svc := params["service"]; svc != "" {
		q.Set("service", svc)
	}
	if scope := params["scope"]; scope != "" {
		q.Set("scope", scope)
	}
	tokenURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL.String(), nil)
	if err != nil {
		return "", err
	}
	if b.cfg.Username != "" {
		req.SetBasicAuth(b.cfg.Username, b.cfg.Password)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching token from %s: %w", realm, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}

	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"` // some registries use this key
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decoding token response: %w", err)
	}

	token := payload.Token
	if token == "" {
		token = payload.AccessToken
	}
	if token == "" {
		return "", fmt.Errorf("token endpoint returned no token")
	}
	return token, nil
}

// parseWWWAuthenticate splits a Www-Authenticate header into scheme and key=value params.
// Example input: `Bearer realm="https://auth.example.com",service="registry",scope="pull"`
func parseWWWAuthenticate(header string) (scheme string, params map[string]string, err error) {
	// Split scheme from the rest at the first space.
	idx := strings.IndexByte(header, ' ')
	if idx < 0 {
		return "", nil, fmt.Errorf("malformed Www-Authenticate: %q", header)
	}
	scheme = strings.TrimSpace(header[:idx])
	rest := strings.TrimSpace(header[idx+1:])

	params = make(map[string]string)
	for _, part := range splitParams(rest) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eqIdx := strings.IndexByte(part, '=')
		if eqIdx < 0 {
			continue
		}
		key := strings.TrimSpace(part[:eqIdx])
		val := strings.Trim(strings.TrimSpace(part[eqIdx+1:]), `"`)
		params[key] = val
	}
	return scheme, params, nil
}

// splitParams splits a comma-separated parameter string, respecting quoted values.
func splitParams(s string) []string {
	var parts []string
	inQuote := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case ',':
			if !inQuote {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, s[start:])
	return parts
}

// registryHost extracts just the host[:port] from a registry URL.
// "https://registry.example.com:5000" → "registry.example.com:5000"
func registryHost(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("no host in URL %q", rawURL)
	}
	return u.Host, nil
}

// insecureTransport returns an http.Transport that skips TLS certificate
// verification. Only used when Config.InsecureSkipTLSVerify is true (e.g.
// local k3d dev clusters with self-signed certs).
func insecureTransport() http.RoundTripper {
	return &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
	}
}
