package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestBackend creates an OCIBackend pointed at the given httptest server.
func newTestBackend(t *testing.T, srv *httptest.Server, cfg Config) *OCIBackend {
	t.Helper()
	cfg.URL = srv.URL
	return NewOCIBackend(cfg)
}

// ---- PushTarget ----

func TestPushTargetBasic(t *testing.T) {
	b := NewOCIBackend(Config{URL: "https://registry.example.com"})
	ref, err := b.PushTarget("my-app", "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref.Registry != "registry.example.com" {
		t.Errorf("Registry = %q, want %q", ref.Registry, "registry.example.com")
	}
	if ref.Path != "mortise/my-app" {
		t.Errorf("Path = %q, want %q", ref.Path, "mortise/my-app")
	}
	if ref.Tag != "abc123" {
		t.Errorf("Tag = %q, want %q", ref.Tag, "abc123")
	}
	want := "registry.example.com/mortise/my-app:abc123"
	if ref.Full != want {
		t.Errorf("Full = %q, want %q", ref.Full, want)
	}
}

func TestPushTargetCustomNamespace(t *testing.T) {
	b := NewOCIBackend(Config{URL: "https://reg.example.com", Namespace: "builds"})
	ref, err := b.PushTarget("svc", "v1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref.Path != "builds/svc" {
		t.Errorf("Path = %q, want %q", ref.Path, "builds/svc")
	}
}

func TestPushTargetEmptyApp(t *testing.T) {
	b := NewOCIBackend(Config{URL: "https://reg.example.com"})
	_, err := b.PushTarget("", "v1")
	if err == nil {
		t.Fatal("expected error for empty app name")
	}
}

func TestPushTargetEmptyTag(t *testing.T) {
	b := NewOCIBackend(Config{URL: "https://reg.example.com"})
	_, err := b.PushTarget("app", "")
	if err == nil {
		t.Fatal("expected error for empty tag")
	}
}

func TestPushTargetInvalidURL(t *testing.T) {
	b := NewOCIBackend(Config{URL: "://bad"})
	_, err := b.PushTarget("app", "v1")
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

// ---- PullSecretRef ----

func TestPullSecretRef(t *testing.T) {
	b := NewOCIBackend(Config{URL: "https://r.example.com", PullSecretName: "my-pull-secret"})
	if got := b.PullSecretRef(); got != "my-pull-secret" {
		t.Errorf("PullSecretRef() = %q, want %q", got, "my-pull-secret")
	}
}

func TestPullSecretRefEmpty(t *testing.T) {
	b := NewOCIBackend(Config{URL: "https://r.example.com"})
	if got := b.PullSecretRef(); got != "" {
		t.Errorf("PullSecretRef() = %q, want empty", got)
	}
}

// ---- Tags ----

func TestTagsList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/mortise/my-app/tags/list" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"name": "mortise/my-app",
			"tags": []string{"v1", "v2", "latest"},
		})
	}))
	defer srv.Close()

	b := newTestBackend(t, srv, Config{})
	tags, err := b.Tags(context.Background(), "my-app")
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) != 3 {
		t.Errorf("expected 3 tags, got %d: %v", len(tags), tags)
	}
}

func TestTagsNotFound(t *testing.T) {
	// 404 should return empty slice, not error — repo may not exist yet.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b := newTestBackend(t, srv, Config{})
	tags, err := b.Tags(context.Background(), "new-app")
	if err != nil {
		t.Fatalf("expected nil error for 404, got: %v", err)
	}
	if tags != nil {
		t.Errorf("expected nil tags for 404, got: %v", tags)
	}
}

func TestTagsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	b := newTestBackend(t, srv, Config{})
	_, err := b.Tags(context.Background(), "app")
	if err == nil {
		t.Fatal("expected error for 500")
	}
}

func TestTagsEmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"name": "mortise/app",
			"tags": nil,
		})
	}))
	defer srv.Close()

	b := newTestBackend(t, srv, Config{})
	tags, err := b.Tags(context.Background(), "app")
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) != 0 {
		t.Errorf("expected empty tags, got %v", tags)
	}
}

// ---- DeleteTag ----

func TestDeleteTagHappyPath(t *testing.T) {
	const digest = "sha256:abc123def456"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead && strings.HasSuffix(r.URL.Path, "/manifests/v1"):
			w.Header().Set("Docker-Content-Digest", digest)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/manifests/"+digest):
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	b := newTestBackend(t, srv, Config{})
	if err := b.DeleteTag(context.Background(), "my-app", "v1"); err != nil {
		t.Fatalf("DeleteTag: %v", err)
	}
}

func TestDeleteTagNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b := newTestBackend(t, srv, Config{})
	err := b.DeleteTag(context.Background(), "app", "missing")
	if err == nil {
		t.Fatal("expected error for non-existent tag")
	}
}

func TestDeleteTagNoDigestHeader(t *testing.T) {
	// Registry responds 200 to HEAD but omits digest — should fail.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	b := newTestBackend(t, srv, Config{})
	err := b.DeleteTag(context.Background(), "app", "v1")
	if err == nil {
		t.Fatal("expected error when digest header is absent")
	}
}

func TestDeleteTagContentDigestFallback(t *testing.T) {
	// Some registries use Content-Digest instead of Docker-Content-Digest.
	const digest = "sha256:feedbeef"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			w.Header().Set("Content-Digest", digest)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()

	b := newTestBackend(t, srv, Config{})
	if err := b.DeleteTag(context.Background(), "app", "v1"); err != nil {
		t.Fatalf("DeleteTag: %v", err)
	}
}

// ---- Auth: Basic ----

func TestBasicAuthForwardedOnRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "admin" || pass != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"tags": []string{"v1"}})
	}))
	defer srv.Close()

	b := newTestBackend(t, srv, Config{Username: "admin", Password: "secret"})
	tags, err := b.Tags(context.Background(), "app")
	if err != nil {
		t.Fatalf("Tags with basic auth: %v", err)
	}
	if len(tags) != 1 || tags[0] != "v1" {
		t.Errorf("unexpected tags: %v", tags)
	}
}

// ---- Auth: Bearer token challenge flow ----

func TestBearerTokenChallenge(t *testing.T) {
	// Simulates: initial request → 401 with Www-Authenticate → token endpoint → retry.
	challenged := false
	const issuedToken = "tok-xyz-789"

	var tokenSrv *httptest.Server
	tokenSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			// Token endpoint
			json.NewEncoder(w).Encode(map[string]string{"token": issuedToken})
			return
		}
		auth := r.Header.Get("Authorization")
		if !challenged {
			// First call: return 401 with Bearer challenge pointing at our token server.
			challenged = true
			challenge := fmt.Sprintf(
				`Bearer realm="%s/token",service="registry.example.com",scope="repository:mortise/app:pull,push"`,
				tokenSrv.URL,
			)
			w.Header().Set("Www-Authenticate", challenge)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Second call: expect the issued token.
		if auth != "Bearer "+issuedToken {
			t.Errorf("retry missing token: got %q", auth)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"tags": []string{"latest"}})
	}))
	defer tokenSrv.Close()

	b := newTestBackend(t, tokenSrv, Config{})
	tags, err := b.Tags(context.Background(), "app")
	if err != nil {
		t.Fatalf("Tags with bearer challenge: %v", err)
	}
	if len(tags) != 1 || tags[0] != "latest" {
		t.Errorf("unexpected tags: %v", tags)
	}
}

func TestBearerTokenChallengeWithCredentials(t *testing.T) {
	// When credentials are configured, they should be sent to the token endpoint.
	const issuedToken = "tok-with-creds"
	var tokenSrv *httptest.Server

	challenged := false
	tokenSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			user, pass, ok := r.BasicAuth()
			if !ok || user != "alice" || pass != "pw" {
				t.Errorf("token endpoint didn't receive basic auth: ok=%v user=%q", ok, user)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"token": issuedToken})
			return
		}
		if !challenged {
			challenged = true
			challenge := fmt.Sprintf(`Bearer realm="%s/token",service="reg"`, tokenSrv.URL)
			w.Header().Set("Www-Authenticate", challenge)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+issuedToken {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"tags": []string{"v2"}})
	}))
	defer tokenSrv.Close()

	b := newTestBackend(t, tokenSrv, Config{Username: "alice", Password: "pw"})
	tags, err := b.Tags(context.Background(), "app")
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) != 1 || tags[0] != "v2" {
		t.Errorf("unexpected tags: %v", tags)
	}
}

func TestAccessTokenFieldFallback(t *testing.T) {
	// Some registries return "access_token" instead of "token".
	const issuedToken = "access-tok-123"
	var tokenSrv *httptest.Server

	challenged := false
	tokenSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			json.NewEncoder(w).Encode(map[string]string{"access_token": issuedToken})
			return
		}
		if !challenged {
			challenged = true
			challenge := fmt.Sprintf(`Bearer realm="%s/token"`, tokenSrv.URL)
			w.Header().Set("Www-Authenticate", challenge)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+issuedToken {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"tags": []string{"v3"}})
	}))
	defer tokenSrv.Close()

	b := newTestBackend(t, tokenSrv, Config{})
	tags, err := b.Tags(context.Background(), "app")
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) != 1 || tags[0] != "v3" {
		t.Errorf("unexpected tags: %v", tags)
	}
}

// ---- parseWWWAuthenticate ----

func TestParseWWWAuthenticate(t *testing.T) {
	cases := []struct {
		name        string
		header      string
		wantScheme  string
		wantParams  map[string]string
		wantErrSubs string
	}{
		{
			name:       "bearer with realm service scope",
			header:     `Bearer realm="https://auth.example.com/token",service="registry.example.com",scope="repository:myapp:pull"`,
			wantScheme: "Bearer",
			wantParams: map[string]string{
				"realm":   "https://auth.example.com/token",
				"service": "registry.example.com",
				"scope":   "repository:myapp:pull",
			},
		},
		{
			name:       "basic auth",
			header:     `Basic realm="Registry"`,
			wantScheme: "Basic",
			wantParams: map[string]string{
				"realm": "Registry",
			},
		},
		{
			name:        "malformed no space",
			header:      "BearerOnlyScheme",
			wantErrSubs: "malformed",
		},
		{
			name:       "bearer realm only",
			header:     `Bearer realm="https://token.example.com"`,
			wantScheme: "Bearer",
			wantParams: map[string]string{
				"realm": "https://token.example.com",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme, params, err := parseWWWAuthenticate(tc.header)
			if tc.wantErrSubs != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrSubs)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubs) {
					t.Errorf("error %q doesn't contain %q", err.Error(), tc.wantErrSubs)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if scheme != tc.wantScheme {
				t.Errorf("scheme = %q, want %q", scheme, tc.wantScheme)
			}
			for k, wantV := range tc.wantParams {
				if gotV := params[k]; gotV != wantV {
					t.Errorf("params[%q] = %q, want %q", k, gotV, wantV)
				}
			}
		})
	}
}

// ---- registryHost ----

func TestRegistryHost(t *testing.T) {
	cases := []struct {
		rawURL  string
		want    string
		wantErr bool
	}{
		{"https://registry.example.com", "registry.example.com", false},
		{"https://registry.example.com:5000", "registry.example.com:5000", false},
		{"http://localhost:5000", "localhost:5000", false},
		{"://bad", "", true},
		{"https://", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.rawURL, func(t *testing.T) {
			got, err := registryHost(tc.rawURL)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.rawURL)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("registryHost(%q) = %q, want %q", tc.rawURL, got, tc.want)
			}
		})
	}
}

// ---- interface compliance ----

// Compile-time check: OCIBackend must implement RegistryBackend.
var _ RegistryBackend = (*OCIBackend)(nil)
