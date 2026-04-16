package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fakeK8sReader is a test double for k8sReader.
type fakeK8sReader struct {
	provider *mortisev1alpha1.GitProvider
	secrets  map[string]string // "ns/name/key" -> value
	apps     []mortisev1alpha1.App
	err      error

	// patched records calls to patchAppRevision: app namespace/name -> sha
	patched map[string]string
}

func (f *fakeK8sReader) getGitProvider(_ context.Context, name string) (*mortisev1alpha1.GitProvider, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.provider == nil {
		return nil, fmt.Errorf("not found")
	}
	return f.provider, nil
}

func (f *fakeK8sReader) getSecret(_ context.Context, namespace, name, key string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	k := namespace + "/" + name + "/" + key
	v, ok := f.secrets[k]
	if !ok {
		return "", fmt.Errorf("secret %s/%s key %q not found", namespace, name, key)
	}
	return v, nil
}

func (f *fakeK8sReader) listGitApps(_ context.Context) ([]mortisev1alpha1.App, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.apps, nil
}

func (f *fakeK8sReader) patchAppRevision(_ context.Context, app *mortisev1alpha1.App, sha string) error {
	if f.patched == nil {
		f.patched = make(map[string]string)
	}
	f.patched[app.Namespace+"/"+app.Name] = sha
	return nil
}

func makeGitProvider(providerType mortisev1alpha1.GitProviderType, secretNS, secretName, secretKey string) *mortisev1alpha1.GitProvider {
	ref := mortisev1alpha1.SecretRef{Namespace: secretNS, Name: secretName, Key: secretKey}
	return &mortisev1alpha1.GitProvider{
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: providerType,
			Host: "https://github.com",
			OAuth: mortisev1alpha1.OAuthConfig{
				ClientIDSecretRef:     ref,
				ClientSecretSecretRef: ref,
			},
			WebhookSecretRef: ref,
		},
	}
}

func makeGitApp(name, ns, repo, branch string) mortisev1alpha1.App {
	return mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:   mortisev1alpha1.SourceTypeGit,
				Repo:   repo,
				Branch: branch,
			},
		},
	}
}

func githubSignature(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func giteaSignature(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func pushPayloadJSON(ref, sha, fullName string) []byte {
	p := map[string]interface{}{
		"ref":   ref,
		"after": sha,
		"repository": map[string]string{
			"full_name": fullName,
		},
	}
	b, _ := json.Marshal(p)
	return b
}

func TestGitHubWebhook_ValidSignature(t *testing.T) {
	const secret = "mysecret"
	const providerName = "github-main"

	body := pushPayloadJSON("refs/heads/main", "abc123def456", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	kr := &fakeK8sReader{
		provider: gp,
		secrets: map[string]string{
			"mortise-system/wh-secret/value": secret,
		},
		apps: []mortisev1alpha1.App{
			makeGitApp("my-app", "project-default", "https://github.com/org/repo", "main"),
		},
	}
	h := New(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))
	req.Header.Set("X-Github-Event", "push")

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}

	if sha := kr.patched["project-default/my-app"]; sha != "abc123def456" {
		t.Errorf("expected my-app patched with sha abc123def456, got %q (all patched: %v)", sha, kr.patched)
	}
}

func TestGitHubWebhook_InvalidSignature(t *testing.T) {
	const secret = "mysecret"
	const providerName = "github-main"

	body := pushPayloadJSON("refs/heads/main", "abc123def456", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	kr := &fakeK8sReader{
		provider: gp,
		secrets: map[string]string{
			"mortise-system/wh-secret/value": secret,
		},
	}
	h := New(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256=invalidsignature")

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestGiteaWebhook_ValidSignature(t *testing.T) {
	const secret = "giteasecret"
	const providerName = "gitea-homelab"

	body := pushPayloadJSON("refs/heads/feature", "deadbeef1234", "user/myrepo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitea, "mortise-system", "wh-secret", "value")
	gp.Spec.Host = "https://gitea.example.com"
	kr := &fakeK8sReader{
		provider: gp,
		secrets: map[string]string{
			"mortise-system/wh-secret/value": secret,
		},
		apps: []mortisev1alpha1.App{
			makeGitApp("my-repo-app", "project-x", "https://gitea.example.com/user/myrepo", "feature"),
		},
	}
	h := New(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitea-Signature", giteaSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}

	if sha := kr.patched["project-x/my-repo-app"]; sha != "deadbeef1234" {
		t.Errorf("expected my-repo-app patched with sha deadbeef1234, got %q", sha)
	}
}

func TestGitLabWebhook_ValidToken(t *testing.T) {
	const secret = "gitlab-webhook-token"
	const providerName = "gitlab-com"

	// GitLab uses checkout_sha rather than after.
	body, _ := json.Marshal(map[string]interface{}{
		"ref":          "refs/heads/main",
		"after":        "0000000000000000000000000000000000000000",
		"checkout_sha": "cafebabe5678",
		"repository": map[string]string{
			"full_name": "ns/project",
		},
	})

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitLab, "mortise-system", "wh-secret", "value")
	gp.Spec.Host = "https://gitlab.com"
	kr := &fakeK8sReader{
		provider: gp,
		secrets: map[string]string{
			"mortise-system/wh-secret/value": secret,
		},
		apps: []mortisev1alpha1.App{
			makeGitApp("gitlab-app", "project-ns", "https://gitlab.com/ns/project", "main"),
		},
	}
	h := New(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Token", secret)

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}

	if sha := kr.patched["project-ns/gitlab-app"]; sha != "cafebabe5678" {
		t.Errorf("expected gitlab-app patched with sha cafebabe5678, got %q", sha)
	}
}

func TestWebhook_ProviderNotFound(t *testing.T) {
	kr := &fakeK8sReader{err: fmt.Errorf("not found")}
	h := New(kr)

	req := httptest.NewRequest(http.MethodPost, "/unknown-provider", http.NoBody)

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

// TestWebhook_DispatchMatrix is a table-driven test covering the dispatch logic:
// which Apps get their revision annotation patched for a given push event.
func TestWebhook_DispatchMatrix(t *testing.T) {
	const secret = "secret"

	appMain := makeGitApp("app-main", "project-a", "https://github.com/org/repo", "main")
	appDev := makeGitApp("app-dev", "project-a", "https://github.com/org/repo", "dev")
	appOtherRepo := makeGitApp("app-other", "project-b", "https://github.com/org/other", "main")

	tests := []struct {
		name        string
		pushRef     string
		pushSHA     string
		pushRepo    string
		apps        []mortisev1alpha1.App
		wantPatched map[string]string // ns/name -> sha; nil means nothing patched
	}{
		{
			name:     "push to main matches app-main only",
			pushRef:  "refs/heads/main",
			pushSHA:  "sha1111",
			pushRepo: "org/repo",
			apps:     []mortisev1alpha1.App{appMain, appDev, appOtherRepo},
			wantPatched: map[string]string{
				"project-a/app-main": "sha1111",
			},
		},
		{
			name:     "push to dev matches app-dev only",
			pushRef:  "refs/heads/dev",
			pushSHA:  "sha2222",
			pushRepo: "org/repo",
			apps:     []mortisev1alpha1.App{appMain, appDev, appOtherRepo},
			wantPatched: map[string]string{
				"project-a/app-dev": "sha2222",
			},
		},
		{
			name:        "push to different repo matches nothing",
			pushRef:     "refs/heads/main",
			pushSHA:     "sha3333",
			pushRepo:    "org/unrelated",
			apps:        []mortisev1alpha1.App{appMain, appDev, appOtherRepo},
			wantPatched: map[string]string{},
		},
		{
			name:     "URL with .git suffix normalizes correctly",
			pushRef:  "refs/heads/main",
			pushSHA:  "sha4444",
			pushRepo: "org/repo",
			apps: []mortisev1alpha1.App{
				makeGitApp("app-giturl", "project-c", "https://github.com/org/repo.git", "main"),
			},
			wantPatched: map[string]string{
				"project-c/app-giturl": "sha4444",
			},
		},
		{
			name:        "no apps returns 202 with no patches",
			pushRef:     "refs/heads/main",
			pushSHA:     "sha5555",
			pushRepo:    "org/repo",
			apps:        []mortisev1alpha1.App{},
			wantPatched: map[string]string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := pushPayloadJSON(tc.pushRef, tc.pushSHA, tc.pushRepo)

			gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
			kr := &fakeK8sReader{
				provider: gp,
				secrets: map[string]string{
					"mortise-system/wh-secret/value": secret,
				},
				apps: tc.apps,
			}
			h := New(kr)

			req := httptest.NewRequest(http.MethodPost, "/github-main", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

			rr := httptest.NewRecorder()
			h.Routes().ServeHTTP(rr, req)

			if rr.Code != http.StatusAccepted {
				t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
			}

			got := kr.patched
			if got == nil {
				got = map[string]string{}
			}

			if len(got) != len(tc.wantPatched) {
				t.Errorf("patched count mismatch: got %v, want %v", got, tc.wantPatched)
				return
			}
			for k, wantSHA := range tc.wantPatched {
				if gotSHA := got[k]; gotSHA != wantSHA {
					t.Errorf("app %s: got sha %q, want %q", k, gotSHA, wantSHA)
				}
			}
		})
	}
}

// TestMatchesWatchPaths is a table-driven test for the monorepo watchPaths gate.
func TestMatchesWatchPaths(t *testing.T) {
	tests := []struct {
		name         string
		watchPaths   []string
		changedPaths []string
		want         bool
	}{
		{
			name:         "empty watchPaths always matches",
			watchPaths:   nil,
			changedPaths: []string{"foo.txt"},
			want:         true,
		},
		{
			name:         "nil changedPaths (no commits key) always matches — backward compat",
			watchPaths:   []string{"services/api"},
			changedPaths: nil,
			want:         true,
		},
		{
			name:         "empty changedPaths (commits present but empty) with watchPaths — no match",
			watchPaths:   []string{"services/api"},
			changedPaths: []string{},
			want:         false,
		},
		{
			name:         "prefix match triggers rebuild",
			watchPaths:   []string{"services/api"},
			changedPaths: []string{"services/api/main.go", "README.md"},
			want:         true,
		},
		{
			name:         "no prefix match, skip rebuild",
			watchPaths:   []string{"services/api"},
			changedPaths: []string{"services/worker/main.go", "README.md"},
			want:         false,
		},
		{
			name:         "leading slash on watchPaths normalized",
			watchPaths:   []string{"/services/api"},
			changedPaths: []string{"services/api/handler.go"},
			want:         true,
		},
		{
			name:         "multiple watchPaths, any-match semantics",
			watchPaths:   []string{"services/api", "shared/"},
			changedPaths: []string{"shared/util.go"},
			want:         true,
		},
		{
			name:         "watchPath of empty string after strip is ignored",
			watchPaths:   []string{"/"},
			changedPaths: []string{"any/file.go"},
			want:         false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := matchesWatchPaths(tc.watchPaths, tc.changedPaths)
			if got != tc.want {
				t.Errorf("matchesWatchPaths(%v, %v) = %v, want %v", tc.watchPaths, tc.changedPaths, got, tc.want)
			}
		})
	}
}

// TestWebhook_WatchPathsGating verifies that a push carrying commits[] only
// triggers rebuilds for Apps whose watchPaths prefix-match at least one
// changed file — while Apps without watchPaths configured still rebuild.
func TestWebhook_WatchPathsGating(t *testing.T) {
	const secret = "secret"

	apiApp := makeGitApp("api", "project-a", "https://github.com/org/repo", "main")
	apiApp.Spec.Source.WatchPaths = []string{"services/api/"}

	workerApp := makeGitApp("worker", "project-a", "https://github.com/org/repo", "main")
	workerApp.Spec.Source.WatchPaths = []string{"services/worker/"}

	unscopedApp := makeGitApp("all", "project-a", "https://github.com/org/repo", "main")
	// No WatchPaths → always rebuilds.

	body, _ := json.Marshal(map[string]interface{}{
		"ref":   "refs/heads/main",
		"after": "sha-api-change",
		"repository": map[string]string{
			"full_name": "org/repo",
		},
		"commits": []map[string]interface{}{
			{
				"added":    []string{"services/api/handler.go"},
				"modified": []string{"README.md"},
				"removed":  []string{},
			},
		},
	})

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{apiApp, workerApp, unscopedApp},
	}
	h := New(kr)

	req := httptest.NewRequest(http.MethodPost, "/github-main", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}

	want := map[string]string{
		"project-a/api": "sha-api-change",
		"project-a/all": "sha-api-change",
	}
	if len(kr.patched) != len(want) {
		t.Fatalf("patched count mismatch: got %v, want %v", kr.patched, want)
	}
	for k, v := range want {
		if got := kr.patched[k]; got != v {
			t.Errorf("app %s: got sha %q, want %q (all patched: %v)", k, got, v, kr.patched)
		}
	}
	if _, skipped := kr.patched["project-a/worker"]; skipped {
		t.Errorf("worker app should have been gated out, but was patched: %v", kr.patched)
	}
}

// TestRepoMatches verifies the URL normalization used for dispatch matching.
func TestRepoMatches(t *testing.T) {
	tests := []struct {
		a, b  string
		match bool
	}{
		// Full URL app repo matches short full_name event repo (the primary webhook case).
		{"https://github.com/org/repo", "org/repo", true},
		// Full URL vs host+path (no scheme).
		{"https://github.com/org/repo", "github.com/org/repo", true},
		// .git suffix is stripped.
		{"https://github.com/org/repo.git", "https://github.com/org/repo", true},
		// Case-insensitive.
		{"https://github.com/Org/Repo", "https://github.com/org/repo", true},
		// Short-form equality.
		{"org/repo", "org/repo", true},
		// Different repo — no match.
		{"https://github.com/org/repo", "org/other", false},
	}
	for _, tc := range tests {
		got := repoMatches(tc.a, tc.b)
		if got != tc.match {
			t.Errorf("repoMatches(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.match)
		}
	}
}
