//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/test/helpers"
)

const (
	// Credentials of the admin user seeded into Mortise for integration tests.
	// The first caller of /api/auth/setup wins; subsequent tests fall through
	// /api/auth/login with the same creds. We pin a single pair so suites that
	// run both tests share a principal.
	mortiseAdminEmail    = "admin-integ@example.invalid"
	mortiseAdminPassword = "integ-admin-pw-01"

	// In-cluster DNS name for the Gitea service. The operator must resolve
	// this — not 127.0.0.1 — when it exchanges the OAuth code for a token.
	giteaInClusterHost = "http://gitea.mortise-test-deps.svc:3000"

	// Gitea admin credentials — provisioned by test/integration/manifests/20-gitea.yaml.
	giteaAdminUser = "mortise-test"
	giteaAdminPw   = "mortise-test-pw"
)

// TestGitProviderAdminAPICRUD exercises the GitProvider admin API end-to-end
// against the running Mortise API in the k3d cluster. It creates, conflicts,
// and deletes a GitProvider, asserting the managed CRD and Secret lifecycle.
func TestGitProviderAdminAPICRUD(t *testing.T) {
	mortisePort := helpers.PortForward(t, "mortise-system", "mortise", 80)
	mortiseURL := fmt.Sprintf("http://127.0.0.1:%d", mortisePort)

	token := helpers.LoginAsAdmin(t, mortiseURL, mortiseAdminEmail, mortiseAdminPassword)

	// Unique name per run so concurrent invocations don't collide. The test
	// namespace helper already gives us test-scoped randomness.
	providerName := "crud-" + strings.ToLower(rand.String(6))

	// Guarantee cleanup before assertions — any early failure still tears down.
	ctx := context.Background()
	t.Cleanup(func() {
		_ = k8sClient.Delete(ctx, &mortisev1alpha1.GitProvider{
			ObjectMeta: metav1.ObjectMeta{Name: providerName},
		})
		_ = k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitprovider-webhook-" + providerName,
				Namespace: "mortise-system",
			},
		})
	})

	body := map[string]any{
		"name":          providerName,
		"type":          "gitea",
		"host":          giteaInClusterHost,
		"clientID":      "stub-client-id",
		"webhookSecret": "stub-webhook-secret",
	}

	// --- POST /api/gitproviders — happy path.
	resp := doJSON(t, http.MethodPost, mortiseURL+"/api/gitproviders", token, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", resp.StatusCode, resp.Body)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(resp.Body), &created); err != nil {
		t.Fatalf("create: decode body: %v", err)
	}
	if created["name"] != providerName {
		t.Errorf("create: name=%v want %s", created["name"], providerName)
	}
	if created["type"] != "gitea" {
		t.Errorf("create: type=%v want gitea", created["type"])
	}
	if created["hasToken"] != false {
		t.Errorf("create: hasToken=%v want false", created["hasToken"])
	}

	// --- CRD must exist.
	var gp mortisev1alpha1.GitProvider
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: providerName}, &gp); err != nil {
		t.Fatalf("get GitProvider CRD: %v", err)
	}
	if gp.Spec.Host != giteaInClusterHost {
		t.Errorf("CRD host=%q want %q", gp.Spec.Host, giteaInClusterHost)
	}

	// --- Managed webhook secret must exist with the API-managed label.
	var secret corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: "mortise-system",
		Name:      "gitprovider-webhook-" + providerName,
	}, &secret); err != nil {
		t.Fatalf("get webhook secret: %v", err)
	}
	if secret.Labels["mortise.dev/managed-by"] != "api" {
		t.Errorf("secret label mortise.dev/managed-by=%q want api",
			secret.Labels["mortise.dev/managed-by"])
	}

	// --- Duplicate POST → 409.
	resp = doJSON(t, http.MethodPost, mortiseURL+"/api/gitproviders", token, body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate create: expected 409, got %d: %s", resp.StatusCode, resp.Body)
	}

	// --- DELETE → 204, then CRD + secret are gone.
	resp = doJSON(t, http.MethodDelete,
		mortiseURL+"/api/gitproviders/"+providerName, token, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d: %s", resp.StatusCode, resp.Body)
	}

	if err := k8sClient.Get(ctx, types.NamespacedName{Name: providerName}, &gp); !errors.IsNotFound(err) {
		t.Errorf("GitProvider still present after delete: err=%v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: "mortise-system",
		Name:      "gitprovider-webhook-" + providerName,
	}, &secret); !errors.IsNotFound(err) {
		t.Errorf("webhook secret still present after delete: err=%v", err)
	}
}

// TestGiteaOAuthFlow exercises the full authorize → consent → callback →
// token-storage path using the in-cluster Gitea as the OAuth provider.
//
// Notable in-flight verifications:
//   - The Mortise authorize endpoint 302s to Gitea's /login/oauth/authorize.
//   - Gitea's consent page (or auto-approve, if TRUSTED=true) eventually 302s
//     back to /api/oauth/{provider}/callback with a code.
//   - The Mortise callback exchanges the code (operator-side call to Gitea's
//     token endpoint over cluster DNS) and writes gitprovider-token-{name}.
//   - The stored token actually works against Gitea's /api/v1/user.
func TestGiteaOAuthFlow(t *testing.T) {
	mortisePort := helpers.PortForward(t, "mortise-system", "mortise", 80)
	giteaPort := helpers.PortForward(t, "mortise-test-deps", "gitea", 3000)

	mortiseURL := fmt.Sprintf("http://127.0.0.1:%d", mortisePort)
	giteaURL := fmt.Sprintf("http://127.0.0.1:%d", giteaPort)

	// Admin JWT for the Mortise API.
	jwt := helpers.LoginAsAdmin(t, mortiseURL, mortiseAdminEmail, mortiseAdminPassword)

	// Ensure Gitea is up and the admin is provisioned before we try to
	// create OAuth apps. Ensure() polls /api/v1/version + basic auth.
	boot := &helpers.GiteaBootstrap{
		BaseURL:  giteaURL,
		Username: giteaAdminUser,
		Password: giteaAdminPw,
	}
	boot.Ensure(t, giteaInClusterHost, giteaAdminUser,
		"oauth-flow-"+strings.ToLower(rand.String(4)),
		map[string]string{"README.md": "oauth flow probe\n"})

	// Unique provider name per run so the callback redirect URI stays stable.
	providerName := "gitea-oauth-" + strings.ToLower(rand.String(6))

	// Register the callback URI that Mortise's OAuth handler will derive from
	// req.Host — it observes 127.0.0.1:{mortisePort} through the port-forward.
	redirectURI := fmt.Sprintf(
		"http://127.0.0.1:%d/api/oauth/%s/callback", mortisePort, providerName)

	app := boot.CreateOAuthApp(t, "mortise-int-"+providerName, []string{redirectURI})
	t.Cleanup(func() { boot.DeleteOAuthApp(t, app.ID) })

	// Create the GitProvider via the Mortise admin API, wiring in the OAuth
	// credentials Gitea just minted.
	ctx := context.Background()
	// hex(mortiseAdminEmail) for per-user token secret cleanup.
	adminEmailHex := hex.EncodeToString([]byte(mortiseAdminEmail))
	t.Cleanup(func() {
		// Best-effort teardown: the server should do this on DELETE, but if the
		// test bails before we reach DELETE the cluster would be left dirty.
		_ = k8sClient.Delete(ctx, &mortisev1alpha1.GitProvider{
			ObjectMeta: metav1.ObjectMeta{Name: providerName},
		})
		_ = k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitprovider-webhook-" + providerName,
				Namespace: "mortise-system",
			},
		})
		_ = k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "user-" + providerName + "-token-" + adminEmailHex,
				Namespace: "mortise-system",
			},
		})
	})

	createBody := map[string]any{
		"name":          providerName,
		"type":          "gitea",
		"host":          giteaInClusterHost,
		"clientID":      app.ClientID,
		"clientSecret":  app.ClientSecret,
		"webhookSecret": "oauth-flow-webhook-secret",
	}
	resp := doJSON(t, http.MethodPost, mortiseURL+"/api/gitproviders", jwt, createBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create GitProvider: expected 201, got %d: %s",
			resp.StatusCode, resp.Body)
	}

	// --- Build a cookie-aware HTTP client that does NOT auto-follow redirects.
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// --- Log into Gitea in the browser sense: CSRF-gated form POST. We need
	// an authenticated Gitea session cookie in the jar so the subsequent
	// /login/oauth/authorize hop sees a logged-in user.
	giteaLogin(t, client, giteaURL, giteaAdminUser, giteaAdminPw)

	// --- Hit Mortise's authorize endpoint; it should redirect us to Gitea's
	// /login/oauth/authorize with client_id + state + redirect_uri.
	authorizeURL := fmt.Sprintf("%s/api/oauth/%s/authorize", mortiseURL, providerName)
	authResp := mustGet(t, client, authorizeURL)
	if authResp.StatusCode != http.StatusFound {
		t.Fatalf("authorize: expected 302, got %d", authResp.StatusCode)
	}
	giteaAuthURL := authResp.Header.Get("Location")
	if giteaAuthURL == "" {
		t.Fatal("authorize: empty Location header")
	}
	// Gitea's public URL is cluster DNS; rewrite to our localhost port-forward
	// so the test client can actually reach it. The redirect URI inside the
	// query string stays 127.0.0.1:{mortisePort} because the OAuth app was
	// registered with that exact callback.
	giteaAuthURL = rewriteToLocal(giteaAuthURL, giteaInClusterHost, giteaURL)

	// --- Follow the redirect to Gitea. Gitea may either show a consent page
	// (HTML form, CSRF-gated) or auto-grant and 302 straight back to the
	// Mortise callback. Handle both.
	consentResp := mustGet(t, client, giteaAuthURL)
	var callbackURL string
	switch consentResp.StatusCode {
	case http.StatusFound, http.StatusSeeOther:
		// Auto-grant path: Location is the Mortise callback.
		callbackURL = consentResp.Header.Get("Location")
	case http.StatusOK:
		// Consent-form path: parse, submit with CSRF.
		callbackURL = giteaConsent(t, client, giteaURL, consentResp)
	default:
		b, _ := io.ReadAll(consentResp.Body)
		consentResp.Body.Close()
		t.Fatalf("gitea authorize: unexpected status %d: %s",
			consentResp.StatusCode, string(b))
	}
	consentResp.Body.Close()

	if callbackURL == "" {
		t.Fatal("oauth: empty callback URL after consent")
	}
	if !strings.Contains(callbackURL, "/api/oauth/"+providerName+"/callback") {
		t.Fatalf("oauth: callback URL %q does not point at mortise callback", callbackURL)
	}

	// --- Follow the callback. Mortise exchanges the code, stores the token,
	// and 302s to /settings/git-providers?connected={name}.
	callbackResp := mustGet(t, client, callbackURL)
	defer callbackResp.Body.Close()
	if callbackResp.StatusCode != http.StatusFound {
		b, _ := io.ReadAll(callbackResp.Body)
		t.Fatalf("callback: expected 302, got %d: %s",
			callbackResp.StatusCode, string(b))
	}
	if loc := callbackResp.Header.Get("Location"); !strings.Contains(loc, "connected="+providerName) {
		t.Errorf("callback: Location=%q want …?connected=%s", loc, providerName)
	}

	// --- Token secret must now exist, be populated, and be a usable Gitea token.
	var tokenSecret corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: "mortise-system",
		Name:      "user-" + providerName + "-token-" + adminEmailHex,
	}, &tokenSecret); err != nil {
		t.Fatalf("get token secret: %v", err)
	}
	tok := string(tokenSecret.Data["token"])
	if tok == "" {
		t.Fatal("token secret: .data.token is empty")
	}

	// Prove the token is valid by calling Gitea's authenticated /user endpoint.
	req, _ := http.NewRequest(http.MethodGet, giteaURL+"/api/v1/user", nil)
	req.Header.Set("Authorization", "token "+tok)
	userResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("gitea /user with OAuth token: %v", err)
	}
	defer userResp.Body.Close()
	if userResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(userResp.Body)
		t.Fatalf("gitea /user status %d: %s", userResp.StatusCode, string(b))
	}

	// --- Delete the provider via the API; token + oauth secrets should go too.
	resp = doJSON(t, http.MethodDelete,
		mortiseURL+"/api/gitproviders/"+providerName, jwt, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete GitProvider: expected 204, got %d: %s",
			resp.StatusCode, resp.Body)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: "mortise-system",
		Name:      "user-" + providerName + "-token-" + adminEmailHex,
	}, &tokenSecret); !errors.IsNotFound(err) {
		t.Errorf("token secret still present after delete: err=%v", err)
	}
}

// --- helpers scoped to this file -----------------------------------------

type httpResult struct {
	StatusCode int
	Body       string
}

// doJSON posts/deletes with a JSON body and Bearer token, returning the parsed
// status + body string. Tests use this instead of the raw http.Client dance
// because none of the admin endpoints use redirects — a plain client is fine.
func doJSON(t *testing.T, method, url, token string, body any) httpResult {
	t.Helper()

	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return httpResult{StatusCode: resp.StatusCode, Body: string(b)}
}

// mustGet issues a GET with the supplied client and fails the test on
// transport error. Caller owns the response body.
func mustGet(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

// csrfInputRE matches Gitea's `<input name="_csrf" value="…">` anywhere on a
// page. Gitea uses both `_csrf` and sometimes a `csrfmiddlewaretoken` cookie;
// the form field is the canonical one.
var csrfInputRE = regexp.MustCompile(`name="_csrf"\s+value="([^"]+)"`)

// formActionRE captures the action attribute of the first <form> tag.
// Good enough for our narrow cases (login, grant); not a real HTML parser.
var formActionRE = regexp.MustCompile(`<form[^>]*action="([^"]+)"`)

// giteaLogin completes the web-login form flow so the cookie jar holds a
// valid session for subsequent OAuth grant.
func giteaLogin(t *testing.T, client *http.Client, giteaURL, user, pw string) {
	t.Helper()

	loginURL := giteaURL + "/user/login"
	resp := mustGet(t, client, loginURL)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("gitea login GET: status %d", resp.StatusCode)
	}
	page, _ := io.ReadAll(resp.Body)

	m := csrfInputRE.FindSubmatch(page)
	if len(m) < 2 {
		t.Fatal("gitea login: _csrf input not found on login page")
	}
	csrf := string(m[1])

	form := url.Values{
		"_csrf":     {csrf},
		"user_name": {user},
		"password":  {pw},
	}
	req, _ := http.NewRequest(http.MethodPost, loginURL,
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	loginResp, err := client.Do(req)
	if err != nil {
		t.Fatalf("gitea login POST: %v", err)
	}
	defer loginResp.Body.Close()
	// A successful login is 302 to "/" (or similar). 200 means Gitea re-rendered
	// the login page with an error — dump the body so the failure is legible.
	if loginResp.StatusCode != http.StatusFound && loginResp.StatusCode != http.StatusSeeOther {
		b, _ := io.ReadAll(loginResp.Body)
		t.Fatalf("gitea login POST: expected 302, got %d: %s",
			loginResp.StatusCode, string(b))
	}
}

// giteaConsent submits the Gitea OAuth consent form (scrape CSRF + hidden
// fields, POST to the form's action). Returns the final Location header
// pointing at Mortise's OAuth callback.
func giteaConsent(t *testing.T, client *http.Client, giteaURL string, page *http.Response) string {
	t.Helper()

	body, err := io.ReadAll(page.Body)
	if err != nil {
		t.Fatalf("read consent page: %v", err)
	}

	m := csrfInputRE.FindSubmatch(body)
	if len(m) < 2 {
		t.Fatalf("consent page: _csrf input not found. Body snippet: %s",
			snippet(body))
	}
	csrf := string(m[1])

	actionMatch := formActionRE.FindSubmatch(body)
	if len(actionMatch) < 2 {
		t.Fatalf("consent page: <form action=…> not found. Body snippet: %s",
			snippet(body))
	}
	action := string(actionMatch[1])
	if !strings.HasPrefix(action, "http") {
		action = giteaURL + action
	}

	// The consent form hides client_id / redirect_uri / state / scope as
	// hidden inputs. Scrape them all with a generic name+value matcher. The
	// form also includes a "granted=true" button; encode that explicitly.
	hidden := hiddenInputs(body)
	hidden.Set("_csrf", csrf)
	hidden.Set("granted", "true")

	req, _ := http.NewRequest(http.MethodPost, action,
		strings.NewReader(hidden.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("gitea consent POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusSeeOther {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("gitea consent POST: expected 302, got %d: %s",
			resp.StatusCode, string(b))
	}
	return resp.Header.Get("Location")
}

// hiddenInputRE finds <input type="hidden" name="…" value="…"> pairs. Gitea's
// emitted order is consistent (type before name before value) but we allow
// any order between the three attributes.
var hiddenInputRE = regexp.MustCompile(
	`<input[^>]*type="hidden"[^>]*name="([^"]+)"[^>]*value="([^"]*)"`,
)

// Some Gitea templates put name before type. Try the alternate ordering too.
var hiddenInputAltRE = regexp.MustCompile(
	`<input[^>]*name="([^"]+)"[^>]*type="hidden"[^>]*value="([^"]*)"`,
)

func hiddenInputs(body []byte) url.Values {
	v := url.Values{}
	for _, m := range hiddenInputRE.FindAllSubmatch(body, -1) {
		v.Set(string(m[1]), string(m[2]))
	}
	for _, m := range hiddenInputAltRE.FindAllSubmatch(body, -1) {
		if _, exists := v[string(m[1])]; !exists {
			v.Set(string(m[1]), string(m[2]))
		}
	}
	return v
}

// snippet returns a bounded printable slice of an HTML body for error logs.
func snippet(b []byte) string {
	const max = 400
	if len(b) < max {
		return string(b)
	}
	return string(b[:max]) + "…"
}

// rewriteToLocal swaps out an in-cluster hostname (e.g. the Gitea DNS name
// baked into the OAuth authorize URL by Gitea's ROOT_URL) for the local
// port-forwarded URL. The query string — and critically, redirect_uri —
// is preserved as-is.
func rewriteToLocal(raw, inCluster, local string) string {
	if strings.HasPrefix(raw, inCluster) {
		return local + strings.TrimPrefix(raw, inCluster)
	}
	return raw
}
