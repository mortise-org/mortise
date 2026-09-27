package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ImageInspector reads an image's config from its registry to answer
// questions the spec doesn't: today, which port the image EXPOSEs. It is
// the image-source counterpart of the build-time DetectedPort (CAI-347) —
// best-effort, read-only, and never load-bearing: callers fall back to
// spec/default ports on any error.
type ImageInspector interface {
	// DetectExposedPort returns the lowest TCP port the image's config
	// exposes, or 0 when the image exposes none. auth is an optional
	// dockerconfigjson blob (a pull secret's .dockerconfigjson data).
	DetectExposedPort(ctx context.Context, imageRef string, dockerConfigJSON []byte) (int32, error)
}

// HTTPImageInspector implements ImageInspector against the OCI Distribution
// Spec with hand-rolled HTTP, matching the rest of this package: token
// challenges are honoured, manifest lists are resolved for this binary's
// platform, and nothing outside GET is ever issued.
type HTTPImageInspector struct {
	// Client defaults to a 15s-timeout client.
	Client *http.Client
}

const inspectBodyCap = 4 << 20 // manifests and configs are KBs; 4MiB is generous

func (h *HTTPImageInspector) client() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// parseImageRef splits an image reference into registry base URL, repository
// path, and reference (tag or digest). Docker Hub shorthand is normalised
// (nginx:1.27 -> registry-1.docker.io, library/nginx).
func parseImageRef(ref string) (baseURL, repo, reference string) {
	reference = "latest"
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		reference = ref[i+1:]
		ref = ref[:i]
	} else if i := strings.LastIndex(ref, ":"); i >= 0 && !strings.Contains(ref[i:], "/") {
		reference = ref[i+1:]
		ref = ref[:i]
	}
	host := "registry-1.docker.io"
	rest := ref
	if i := strings.Index(ref, "/"); i >= 0 {
		first := ref[:i]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			host = first
			rest = ref[i+1:]
		}
	}
	if host == "docker.io" {
		host = "registry-1.docker.io"
	}
	if host == "registry-1.docker.io" && !strings.Contains(rest, "/") {
		rest = "library/" + rest
	}
	scheme := "https"
	if strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1") {
		scheme = "http"
	}
	return scheme + "://" + host, rest, reference
}

// registryAuth carries a resolved Authorization header value for one fetch.
type registryAuth struct {
	header string
}

// get issues a GET, resolving one 401 token challenge (anonymous, or basic
// credentials from the pull secret) the same way OCIBackend does.
func (h *HTTPImageInspector) get(ctx context.Context, rawURL, accept string, basicUser, basicPass string, auth *registryAuth) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if auth.header != "" {
		req.Header.Set("Authorization", auth.header)
	}
	resp, err := h.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	_ = resp.Body.Close()
	scheme, params, err := parseWWWAuthenticate(challenge)
	if err != nil {
		return nil, fmt.Errorf("unauthorized and unparsable challenge: %w", err)
	}
	switch strings.ToLower(scheme) {
	case "basic":
		if basicUser == "" {
			return nil, fmt.Errorf("registry requires basic auth and no pull secret matched")
		}
		auth.header = "Basic " + base64.StdEncoding.EncodeToString([]byte(basicUser+":"+basicPass))
	case "bearer":
		token, err := h.fetchToken(ctx, params, basicUser, basicPass)
		if err != nil {
			return nil, err
		}
		auth.header = "Bearer " + token
	default:
		return nil, fmt.Errorf("unsupported auth scheme %q", scheme)
	}
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req2.Header.Set("Accept", accept)
	}
	req2.Header.Set("Authorization", auth.header)
	return h.client().Do(req2)
}

func (h *HTTPImageInspector) fetchToken(ctx context.Context, params map[string]string, user, pass string) (string, error) {
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("bearer challenge without realm")
	}
	tokenURL := realm
	sep := "?"
	if strings.Contains(realm, "?") {
		sep = "&"
	}
	if s := params["service"]; s != "" {
		tokenURL += sep + "service=" + s
		sep = "&"
	}
	if sc := params["scope"]; sc != "" {
		tokenURL += sep + "scope=" + sc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return "", err
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := h.client().Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, inspectBodyCap)).Decode(&body); err != nil {
		return "", err
	}
	if body.Token != "" {
		return body.Token, nil
	}
	return body.AccessToken, nil
}

// credsFor extracts basic credentials for a registry host from a
// dockerconfigjson blob. Missing entries mean anonymous.
func credsFor(dockerConfigJSON []byte, baseURL string) (user, pass string) {
	if len(dockerConfigJSON) == 0 {
		return "", ""
	}
	var cfg struct {
		Auths map[string]struct {
			Auth     string `json:"auth"`
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(dockerConfigJSON, &cfg); err != nil {
		return "", ""
	}
	host := strings.TrimPrefix(strings.TrimPrefix(baseURL, "https://"), "http://")
	for key, a := range cfg.Auths {
		k := strings.TrimPrefix(strings.TrimPrefix(key, "https://"), "http://")
		k = strings.TrimSuffix(k, "/")
		if k != host && !(host == "registry-1.docker.io" && (strings.Contains(k, "docker.io") || strings.Contains(k, "index.docker.io"))) {
			continue
		}
		if a.Username != "" {
			return a.Username, a.Password
		}
		if a.Auth != "" {
			if dec, err := base64.StdEncoding.DecodeString(a.Auth); err == nil {
				if i := strings.IndexByte(string(dec), ':'); i >= 0 {
					return string(dec[:i]), string(dec[i+1:])
				}
			}
		}
	}
	return "", ""
}

const inspectAccept = "application/vnd.oci.image.index.v1+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.docker.distribution.manifest.v2+json"

func (h *HTTPImageInspector) DetectExposedPort(ctx context.Context, imageRef string, dockerConfigJSON []byte) (int32, error) {
	baseURL, repo, reference := parseImageRef(imageRef)
	user, pass := credsFor(dockerConfigJSON, baseURL)
	auth := &registryAuth{}

	manifest, err := h.fetchJSON(ctx, baseURL+"/v2/"+repo+"/manifests/"+reference, inspectAccept, user, pass, auth)
	if err != nil {
		return 0, fmt.Errorf("fetch manifest: %w", err)
	}

	// Manifest list / OCI index: pick this platform's entry (linux + our arch).
	if manifests, ok := manifest["manifests"].([]any); ok {
		digest := pickPlatformDigest(manifests)
		if digest == "" {
			return 0, fmt.Errorf("no linux/%s entry in manifest list", runtime.GOARCH)
		}
		manifest, err = h.fetchJSON(ctx, baseURL+"/v2/"+repo+"/manifests/"+digest, inspectAccept, user, pass, auth)
		if err != nil {
			return 0, fmt.Errorf("fetch platform manifest: %w", err)
		}
	}

	cfg, ok := manifest["config"].(map[string]any)
	if !ok {
		return 0, fmt.Errorf("manifest has no config descriptor")
	}
	cfgDigest, _ := cfg["digest"].(string)
	if cfgDigest == "" {
		return 0, fmt.Errorf("config descriptor has no digest")
	}

	imgCfg, err := h.fetchJSON(ctx, baseURL+"/v2/"+repo+"/blobs/"+cfgDigest, "", user, pass, auth)
	if err != nil {
		return 0, fmt.Errorf("fetch image config: %w", err)
	}
	inner, _ := imgCfg["config"].(map[string]any)
	exposed, _ := inner["ExposedPorts"].(map[string]any)

	var ports []int
	for spec := range exposed {
		portStr, proto, found := strings.Cut(spec, "/")
		if found && proto != "tcp" {
			continue
		}
		if p, err := strconv.Atoi(portStr); err == nil && p > 0 && p < 65536 {
			ports = append(ports, p)
		}
	}
	if len(ports) == 0 {
		return 0, nil
	}
	sort.Ints(ports)
	return int32(ports[0]), nil
}

func pickPlatformDigest(manifests []any) string {
	var firstLinux string
	for _, m := range manifests {
		entry, ok := m.(map[string]any)
		if !ok {
			continue
		}
		platform, _ := entry["platform"].(map[string]any)
		osName, _ := platform["os"].(string)
		arch, _ := platform["architecture"].(string)
		digest, _ := entry["digest"].(string)
		if osName != "linux" || digest == "" {
			continue
		}
		if firstLinux == "" {
			firstLinux = digest
		}
		if arch == runtime.GOARCH {
			return digest
		}
	}
	return firstLinux
}

func (h *HTTPImageInspector) fetchJSON(ctx context.Context, url, accept, user, pass string, auth *registryAuth) (map[string]any, error) {
	resp, err := h.get(ctx, url, accept, user, pass, auth)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %d", url, resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, inspectBodyCap)).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
