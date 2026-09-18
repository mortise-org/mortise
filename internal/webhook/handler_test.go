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
	"strings"
	"testing"
	"time"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/git"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	testclock "k8s.io/utils/clock/testing"
)

// fakeK8sReader is a test double for k8sReader.
type fakeK8sReader struct {
	signatureWrites []string // "provider=mismatch" per setWebhookSignatureCondition call
	provider        *mortisev1alpha1.GitProvider
	secrets         map[string]string // "ns/name/key" -> value
	apps            []mortisev1alpha1.App
	projects        map[string]*mortisev1alpha1.Project // name -> project
	err             error

	// providerCount overrides the derived GitProvider count when > 0.
	providerCount int

	// patched records calls to patchAppRevision: app namespace/name -> sha
	patched map[string]string

	// preview environment tracking
	previewEnvs     map[string]*mortisev1alpha1.PreviewEnvironment // "ns/name" -> PE
	createdPreviews []mortisev1alpha1.PreviewEnvironment
	updatedPreviews []mortisev1alpha1.PreviewEnvironment
	deletedKeys     []string // "ns/name" keys
}

// providerCount overrides countGitProviders when set; otherwise the count
// mirrors whether `provider` is populated (the single-provider default that
// keeps empty-providerRef fixtures matching, as they did pre-policy).
func (f *fakeK8sReader) setWebhookSignatureCondition(_ context.Context, providerName string, mismatch bool, _ time.Time) error {
	f.signatureWrites = append(f.signatureWrites, fmt.Sprintf("%s=%t", providerName, mismatch))
	return nil
}

func (f *fakeK8sReader) countGitProviders(_ context.Context) (int, error) {
	if f.providerCount > 0 {
		return f.providerCount, nil
	}
	if f.provider != nil {
		return 1, nil
	}
	return 0, nil
}

func (f *fakeK8sReader) getGitProvider(_ context.Context, name string) (*mortisev1alpha1.GitProvider, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.provider == nil {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "mortise.mortise.dev", Resource: "gitproviders"}, name)
	}
	return f.provider, nil
}

func (f *fakeK8sReader) getProject(_ context.Context, name string) (*mortisev1alpha1.Project, error) {
	if f.err != nil {
		return nil, f.err
	}
	p, ok := f.projects[name]
	if !ok {
		return nil, fmt.Errorf("project %q not found", name)
	}
	return p, nil
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

func (f *fakeK8sReader) getPreviewEnvironment(_ context.Context, namespace, name string) (*mortisev1alpha1.PreviewEnvironment, error) {
	if f.err != nil {
		return nil, f.err
	}
	key := namespace + "/" + name
	if pe, ok := f.previewEnvs[key]; ok {
		return pe, nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: "mortise.dev", Resource: "previewenvironments"}, name)
}

func (f *fakeK8sReader) createPreviewEnvironment(_ context.Context, pe *mortisev1alpha1.PreviewEnvironment) error {
	f.createdPreviews = append(f.createdPreviews, *pe)
	return nil
}

func (f *fakeK8sReader) updatePreviewEnvironment(_ context.Context, pe *mortisev1alpha1.PreviewEnvironment) error {
	f.updatedPreviews = append(f.updatedPreviews, *pe)
	return nil
}

func (f *fakeK8sReader) deletePreviewEnvironment(_ context.Context, namespace, name string) error {
	f.deletedKeys = append(f.deletedKeys, namespace+"/"+name)
	return nil
}

func newTestHandler(kr *fakeK8sReader) *Handler {
	h := New(kr)
	h.gitAPIFromProvider = func(*mortisev1alpha1.GitProvider, string, string) (git.GitAPI, error) {
		return &testGitAPI{}, nil
	}
	return h
}

type testGitAPI struct{}

func (t *testGitAPI) RegisterWebhook(context.Context, string, git.WebhookConfig) error { return nil }
func (t *testGitAPI) ListWebhooks(context.Context, string) ([]git.WebhookInfo, error) {
	return nil, nil
}
func (t *testGitAPI) DeleteWebhook(context.Context, string, int64) error { return nil }
func (t *testGitAPI) PostCommitStatus(context.Context, string, string, git.CommitStatus) error {
	return nil
}
func (t *testGitAPI) VerifyWebhookSignature([]byte, http.Header) error { return nil }
func (t *testGitAPI) ResolveCloneCredentials(context.Context, string) (git.GitCredentials, error) {
	return git.GitCredentials{}, nil
}
func (t *testGitAPI) ListRepos(context.Context) ([]git.Repository, error) { return nil, nil }
func (t *testGitAPI) ListBranches(context.Context, string) ([]git.Branch, error) {
	return nil, nil
}
func (t *testGitAPI) ResolveBranchHead(context.Context, string, string) (string, error) {
	return "", nil
}
func (t *testGitAPI) ListOpenPullRequests(context.Context, string) ([]git.PullRequestSnapshot, error) {
	return nil, nil
}
func (t *testGitAPI) ListTree(context.Context, string, string, string, string) ([]git.TreeEntry, error) {
	return nil, nil
}

func makeGitProvider(providerType mortisev1alpha1.GitProviderType, secretNS, secretName, secretKey string) *mortisev1alpha1.GitProvider {
	ref := mortisev1alpha1.SecretRef{Namespace: secretNS, Name: secretName, Key: secretKey}
	return &mortisev1alpha1.GitProvider{
		Spec: mortisev1alpha1.GitProviderSpec{
			Type:             providerType,
			Host:             "https://github.com",
			WebhookSecretRef: &ref,
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
			makeGitApp("my-app", "pj-default", "https://github.com/org/repo", "main"),
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

	if sha := kr.patched["pj-default/my-app"]; sha != "abc123def456" {
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
			makeGitApp("my-repo-app", "pj-x", "https://gitea.example.com/user/myrepo", "feature"),
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

	if sha := kr.patched["pj-x/my-repo-app"]; sha != "deadbeef1234" {
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
			makeGitApp("gitlab-app", "pj-ns", "https://gitlab.com/ns/project", "main"),
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

	if sha := kr.patched["pj-ns/gitlab-app"]; sha != "cafebabe5678" {
		t.Errorf("expected gitlab-app patched with sha cafebabe5678, got %q", sha)
	}
}

// TestWebhook_PreVerificationFailuresAreUniform pins the anti-enumeration
// contract: an unauthenticated caller gets the identical 401 body whether the
// provider does not exist, exists without a webhook secret, or fails
// signature verification — no probing which provider names are registered.
func TestWebhook_PreVerificationFailuresAreUniform(t *testing.T) {
	cases := []struct {
		name string
		kr   *fakeK8sReader
	}{
		{name: "provider not found", kr: &fakeK8sReader{
			err: apierrors.NewNotFound(schema.GroupResource{Group: "mortise.mortise.dev", Resource: "gitproviders"}, "some-provider"),
		}},
		{name: "webhook secret not configured", kr: &fakeK8sReader{
			provider: &mortisev1alpha1.GitProvider{
				Spec: mortisev1alpha1.GitProviderSpec{Type: mortisev1alpha1.GitProviderTypeGitea},
			},
		}},
		{name: "invalid signature", kr: &fakeK8sReader{
			provider: makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "hook", "secret"),
			secrets:  map[string]string{"mortise-system/hook/secret": "topsecret"},
		}},
	}

	var wantBody string
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := New(tc.kr)
			req := httptest.NewRequest(http.MethodPost, "/some-provider", strings.NewReader("{}"))

			rr := httptest.NewRecorder()
			h.Routes().ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("expected uniform 401, got %d", rr.Code)
			}
			if i == 0 {
				wantBody = rr.Body.String()
			} else if rr.Body.String() != wantBody {
				t.Fatalf("response bodies differ between pre-verification failures: %q vs %q", rr.Body.String(), wantBody)
			}
		})
	}
}

// TestWebhook_DispatchMatrix is a table-driven test covering the dispatch logic:
// which Apps get their revision annotation patched for a given push event.
func TestWebhook_DispatchMatrix(t *testing.T) {
	const secret = "secret"

	appMain := makeGitApp("app-main", "pj-a", "https://github.com/org/repo", "main")
	appDev := makeGitApp("app-dev", "pj-a", "https://github.com/org/repo", "dev")
	appOtherRepo := makeGitApp("app-other", "pj-b", "https://github.com/org/other", "main")

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
				"pj-a/app-main": "sha1111",
			},
		},
		{
			name:     "push to dev matches app-dev only",
			pushRef:  "refs/heads/dev",
			pushSHA:  "sha2222",
			pushRepo: "org/repo",
			apps:     []mortisev1alpha1.App{appMain, appDev, appOtherRepo},
			wantPatched: map[string]string{
				"pj-a/app-dev": "sha2222",
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
				makeGitApp("app-giturl", "pj-c", "https://github.com/org/repo.git", "main"),
			},
			wantPatched: map[string]string{
				"pj-c/app-giturl": "sha4444",
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
			h := newTestHandler(kr)

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

	apiApp := makeGitApp("api", "pj-a", "https://github.com/org/repo", "main")
	apiApp.Spec.Source.WatchPaths = []string{"services/api/"}

	workerApp := makeGitApp("worker", "pj-a", "https://github.com/org/repo", "main")
	workerApp.Spec.Source.WatchPaths = []string{"services/worker/"}

	unscopedApp := makeGitApp("all", "pj-a", "https://github.com/org/repo", "main")
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
		"pj-a/api": "sha-api-change",
		"pj-a/all": "sha-api-change",
	}
	if len(kr.patched) != len(want) {
		t.Fatalf("patched count mismatch: got %v, want %v", kr.patched, want)
	}
	for k, v := range want {
		if got := kr.patched[k]; got != v {
			t.Errorf("app %s: got sha %q, want %q (all patched: %v)", k, got, v, kr.patched)
		}
	}
	if _, skipped := kr.patched["pj-a/worker"]; skipped {
		t.Errorf("worker app should have been gated out, but was patched: %v", kr.patched)
	}
}

func TestWebhook_PushHonorsProviderRef(t *testing.T) {
	const secret = "secret"

	providerAApp := makeGitApp("app-a", "pj-a", "https://github.com/org/repo", "main")
	providerAApp.Spec.Source.ProviderRef = "provider-a"
	providerBApp := makeGitApp("app-b", "pj-a", "https://github.com/org/repo", "main")
	providerBApp.Spec.Source.ProviderRef = "provider-b"

	body, _ := json.Marshal(map[string]interface{}{
		"ref":   "refs/heads/main",
		"after": "sha-provider-a",
		"repository": map[string]string{
			"full_name": "org/repo",
		},
	})

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{providerAApp, providerBApp},
	}
	h := New(kr)

	req := httptest.NewRequest(http.MethodPost, "/provider-a", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if got := kr.patched["pj-a/app-a"]; got != "sha-provider-a" {
		t.Fatalf("provider-a app patched sha = %q, want sha-provider-a", got)
	}
	if _, ok := kr.patched["pj-a/app-b"]; ok {
		t.Fatalf("provider-b app should not be patched from provider-a webhook: %+v", kr.patched)
	}
}

// ---------------------------------------------------------------------------
// PR event tests
// ---------------------------------------------------------------------------

// githubPRPayloadJSON returns a GitHub pull_request event body.
func githubPRPayloadJSON(action string, number int, branch, sha, fullName string) []byte {
	p := map[string]interface{}{
		"action": action,
		"number": number,
		"pull_request": map[string]interface{}{
			"number": number,
			"head": map[string]string{
				"ref": branch,
				"sha": sha,
			},
		},
		"repository": map[string]string{
			"full_name": fullName,
		},
	}
	b, _ := json.Marshal(p)
	return b
}

// gitlabMRPayloadJSON returns a GitLab Merge Request Hook body.
func gitlabMRPayloadJSON(action, state string, iid int, sourceBranch, sha, fullName string) []byte {
	p := map[string]interface{}{
		"object_attributes": map[string]interface{}{
			"action":        action,
			"state":         state,
			"iid":           iid,
			"source_branch": sourceBranch,
			"last_commit": map[string]string{
				"id": sha,
			},
		},
		"repository": map[string]string{
			"full_name": fullName,
		},
	}
	b, _ := json.Marshal(p)
	return b
}

// makePreviewGitApp builds an App plus a Project with preview enabled. The
// Project name is derived by stripping the "pj-" prefix from ns.
// The project always declares a staging env — preview env creation requires
// staging to exist on the project.
func makePreviewGitApp(name, ns, repo, branch string, _, _ string) (mortisev1alpha1.App, *mortisev1alpha1.Project) {
	app := makeGitApp(name, ns, repo, branch)
	projectName := strings.TrimPrefix(ns, "pj-")
	proj := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: projectName},
		Spec: mortisev1alpha1.ProjectSpec{
			Environments: []mortisev1alpha1.ProjectEnvironment{
				{Name: "production"},
				{Name: "staging", DisplayOrder: 1},
			},
			Preview: &mortisev1alpha1.PreviewConfig{
				Enabled: true,
			},
		},
	}
	return app, proj
}

// makeProject builds a Project CR with optional preview config.
func makeProject(name string, preview *mortisev1alpha1.PreviewConfig) *mortisev1alpha1.Project {
	return &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       mortisev1alpha1.ProjectSpec{Preview: preview},
	}
}

func TestGitHubPREvent_Opened_CreatesPreviewEnvironment(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	body := githubPRPayloadJSON("opened", 42, "feature/x", "shaopened", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	app, proj := makePreviewGitApp("my-app", "pj-default", "https://github.com/org/repo", "main", "", "")
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"default": proj},
	}
	h := newTestHandler(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.createdPreviews) != 1 {
		t.Fatalf("expected 1 PE created, got %d", len(kr.createdPreviews))
	}
	pe := kr.createdPreviews[0]
	if pe.Name != "preview-pr-42" {
		t.Errorf("unexpected PE name: %q", pe.Name)
	}
	if pe.Namespace != "pj-default" {
		t.Errorf("unexpected PE namespace: %q", pe.Namespace)
	}
	if pe.Spec.ProjectRef != "default" {
		t.Errorf("projectRef mismatch: %q", pe.Spec.ProjectRef)
	}
	if pe.Spec.SourceEnv != "staging" {
		t.Errorf("sourceEnv mismatch: %q", pe.Spec.SourceEnv)
	}
	if pe.Spec.PullRequest.Number != 42 {
		t.Errorf("PR number mismatch: %d", pe.Spec.PullRequest.Number)
	}
	if pe.Spec.PullRequest.Branch != "feature/x" {
		t.Errorf("branch mismatch: %q", pe.Spec.PullRequest.Branch)
	}
	if pe.Spec.PullRequest.SHA != "shaopened" {
		t.Errorf("sha mismatch: %q", pe.Spec.PullRequest.SHA)
	}
}

func TestGitHubPREvent_Reopened_CreatesPreviewEnvironment(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	body := githubPRPayloadJSON("reopened", 42, "feature/x", "shareopened", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	app, proj := makePreviewGitApp("my-app", "pj-default", "https://github.com/org/repo", "main", "", "")
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"default": proj},
	}
	h := newTestHandler(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.createdPreviews) != 1 {
		t.Fatalf("expected 1 PE created, got %d", len(kr.createdPreviews))
	}
	pe := kr.createdPreviews[0]
	if pe.Name != "preview-pr-42" {
		t.Errorf("unexpected PE name: %q", pe.Name)
	}
	if pe.Namespace != "pj-default" {
		t.Errorf("unexpected PE namespace: %q", pe.Namespace)
	}
	if pe.Spec.ProjectRef != "default" {
		t.Errorf("projectRef mismatch: %q", pe.Spec.ProjectRef)
	}
	if pe.Spec.SourceEnv != "staging" {
		t.Errorf("sourceEnv mismatch: %q", pe.Spec.SourceEnv)
	}
	if pe.Spec.PullRequest.Number != 42 {
		t.Errorf("PR number mismatch: %d", pe.Spec.PullRequest.Number)
	}
	if pe.Spec.PullRequest.Branch != "feature/x" {
		t.Errorf("branch mismatch: %q", pe.Spec.PullRequest.Branch)
	}
	if pe.Spec.PullRequest.SHA != "shareopened" {
		t.Errorf("sha mismatch: %q", pe.Spec.PullRequest.SHA)
	}
}

func TestGitHubPREvent_MultiRepoUsesConvergencePreviewNameForCreateUpdateDelete(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"
	const eventRepo = "org/foo.bar"
	const appRepo = "https://github.com/org/foo.bar"

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	targetApp := makeGitApp("target", "pj-default", appRepo, "main")
	otherApp := makeGitApp("other", "pj-default", "https://github.com/org/other-repo", "main")
	proj := makeProject("default", &mortisev1alpha1.PreviewConfig{Enabled: true})
	proj.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}}
	expectedName := constants.PreviewEnvironmentName(appRepo, 42, true)

	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{targetApp, otherApp},
		projects: map[string]*mortisev1alpha1.Project{"default": proj},
	}
	h := newTestHandler(kr)

	openedBody := githubPRPayloadJSON("opened", 42, "feature/x", "sha-opened", eventRepo)
	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(openedBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", githubSignature(openedBody, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("opened: expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.createdPreviews) != 1 {
		t.Fatalf("opened: expected 1 PE created, got %d", len(kr.createdPreviews))
	}
	if kr.createdPreviews[0].Name != expectedName {
		t.Fatalf("opened: expected PE name %q, got %q", expectedName, kr.createdPreviews[0].Name)
	}

	existing := kr.createdPreviews[0]
	kr.previewEnvs = map[string]*mortisev1alpha1.PreviewEnvironment{
		"pj-default/" + expectedName: &existing,
	}

	syncBody := githubPRPayloadJSON("synchronize", 42, "feature/x", "sha-sync", eventRepo)
	req = httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(syncBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", githubSignature(syncBody, secret))

	rr = httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("synchronize: expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.updatedPreviews) != 1 {
		t.Fatalf("synchronize: expected 1 PE updated, got %d", len(kr.updatedPreviews))
	}
	if kr.updatedPreviews[0].Name != expectedName {
		t.Fatalf("synchronize: expected update to target %q, got %q", expectedName, kr.updatedPreviews[0].Name)
	}
	if kr.updatedPreviews[0].Spec.PullRequest.SHA != "sha-sync" {
		t.Fatalf("synchronize: expected SHA sha-sync, got %q", kr.updatedPreviews[0].Spec.PullRequest.SHA)
	}

	closedBody := githubPRPayloadJSON("closed", 42, "feature/x", "sha-sync", eventRepo)
	req = httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(closedBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", githubSignature(closedBody, secret))

	rr = httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("closed: expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.deletedKeys) != 1 {
		t.Fatalf("closed: expected 1 PE delete, got %d", len(kr.deletedKeys))
	}
	if kr.deletedKeys[0] != "pj-default/"+expectedName {
		t.Fatalf("closed: expected delete key %q, got %q", "pj-default/"+expectedName, kr.deletedKeys[0])
	}
}

func TestGitHubPREvent_MixedRepoSyntaxStaysSingleRepo(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	appA := makeGitApp("target", "pj-default", "https://github.com/org/repo.git", "main")
	appB := makeGitApp("other", "pj-default", "git@github.com:org/repo.git", "main")
	proj := makeProject("default", &mortisev1alpha1.PreviewConfig{Enabled: true})
	proj.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}}

	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{appA, appB},
		projects: map[string]*mortisev1alpha1.Project{"default": proj},
	}
	h := newTestHandler(kr)

	body := githubPRPayloadJSON("opened", 42, "feature/x", "sha-opened", "org/repo")
	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("opened: expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.createdPreviews) != 1 {
		t.Fatalf("opened: expected 1 PE created, got %d", len(kr.createdPreviews))
	}
	if kr.createdPreviews[0].Name != "preview-pr-42" {
		t.Fatalf("opened: expected legacy single-repo PE name, got %q", kr.createdPreviews[0].Name)
	}
}

func TestGiteaPREvent_Opened_CreatesPreviewEnvironment(t *testing.T) {
	const secret = "giteaprsecret"
	const providerName = "gitea-homelab"

	body := githubPRPayloadJSON("opened", 7, "topic/feat", "gitasha", "user/myrepo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitea, "mortise-system", "wh-secret", "value")
	gp.Spec.Host = "https://gitea.example.com"
	app, proj := makePreviewGitApp("myrepo-app", "pj-gitea", "https://gitea.example.com/user/myrepo", "main", "", "")
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"gitea": proj},
	}
	h := newTestHandler(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitea-Event", "pull_request")
	req.Header.Set("X-Gitea-Signature", giteaSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.createdPreviews) != 1 {
		t.Fatalf("expected 1 PE created, got %d", len(kr.createdPreviews))
	}
	pe := kr.createdPreviews[0]
	if pe.Name != "preview-pr-7" {
		t.Errorf("unexpected PE name: %q", pe.Name)
	}
	if pe.Spec.PullRequest.Number != 7 || pe.Spec.PullRequest.SHA != "gitasha" {
		t.Errorf("PR ref mismatch: %+v", pe.Spec.PullRequest)
	}
	if pe.Spec.ProjectRef != "gitea" {
		t.Errorf("projectRef mismatch: %q", pe.Spec.ProjectRef)
	}
}

func TestGitLabPREvent_Opened_CreatesPreviewEnvironment(t *testing.T) {
	const secret = "gitlabprsecret"
	const providerName = "gitlab-com"

	body := gitlabMRPayloadJSON("open", "opened", 11, "feat/branch", "mrsha1", "ns/project")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitLab, "mortise-system", "wh-secret", "value")
	gp.Spec.Host = "https://gitlab.com"
	app, proj := makePreviewGitApp("gl-app", "pj-gl", "https://gitlab.com/ns/project", "main", "", "")
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"gl": proj},
	}
	h := newTestHandler(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
	req.Header.Set("X-Gitlab-Token", secret)

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.createdPreviews) != 1 {
		t.Fatalf("expected 1 PE created, got %d", len(kr.createdPreviews))
	}
	pe := kr.createdPreviews[0]
	if pe.Name != "preview-pr-11" {
		t.Errorf("unexpected PE name: %q", pe.Name)
	}
	if pe.Spec.PullRequest.Number != 11 || pe.Spec.PullRequest.Branch != "feat/branch" || pe.Spec.PullRequest.SHA != "mrsha1" {
		t.Errorf("PR ref mismatch: %+v", pe.Spec.PullRequest)
	}
	if pe.Spec.ProjectRef != "gl" {
		t.Errorf("projectRef mismatch: %q", pe.Spec.ProjectRef)
	}
}

func TestPREvent_ProjectPreviewDisabled_NoPECreated(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	body := githubPRPayloadJSON("opened", 5, "f", "sha", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	app := makeGitApp("no-preview", "pj-default", "https://github.com/org/repo", "main")
	// preview nil → disabled.
	proj := makeProject("default", nil)
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"default": proj},
	}
	h := newTestHandler(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.createdPreviews) != 0 {
		t.Errorf("expected no PE created, got %d", len(kr.createdPreviews))
	}

	// Now test with preview explicitly disabled on the Project.
	app2 := makeGitApp("also-no-preview", "pj-default", "https://github.com/org/repo", "main")
	proj2 := makeProject("default", &mortisev1alpha1.PreviewConfig{Enabled: false})
	kr2 := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app2},
		projects: map[string]*mortisev1alpha1.Project{"default": proj2},
	}
	h2 := newTestHandler(kr2)

	req2 := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-GitHub-Event", "pull_request")
	req2.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr2 := httptest.NewRecorder()
	h2.Routes().ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr2.Code, rr2.Body.String())
	}
	if len(kr2.createdPreviews) != 0 {
		t.Errorf("expected no PE created with preview.enabled=false, got %d", len(kr2.createdPreviews))
	}
}

func TestPREvent_OpenedHonorsProviderRef(t *testing.T) {
	const secret = "prsecret"
	body := githubPRPayloadJSON("opened", 42, "feature/x", "shaopened", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	// app-a in pj-default matches provider-a, app-b in pj-other matches provider-b.
	// Only app-a's project should get a PE since the webhook comes from provider-a.
	appA, projA := makePreviewGitApp("app-a", "pj-default", "https://github.com/org/repo", "main", "", "")
	appA.Spec.Source.ProviderRef = "provider-a"
	appB, _ := makePreviewGitApp("app-b", "pj-other", "https://github.com/org/repo", "main", "", "")
	appB.Spec.Source.ProviderRef = "provider-b"
	projB := makeProject("other", &mortisev1alpha1.PreviewConfig{Enabled: true})
	projB.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}}

	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{appA, appB},
		projects: map[string]*mortisev1alpha1.Project{"default": projA, "other": projB},
	}
	h := newTestHandler(kr)

	req := httptest.NewRequest(http.MethodPost, "/provider-a", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.createdPreviews) != 1 {
		t.Fatalf("expected exactly 1 preview create, got %d", len(kr.createdPreviews))
	}
	if got := kr.createdPreviews[0].Spec.ProjectRef; got != "default" {
		t.Fatalf("preview created for project %q, want default", got)
	}
}

func TestPREvent_ClosedDeletesPreviewWhenPreviewDisabled(t *testing.T) {
	const secret = "prsecret"
	body := githubPRPayloadJSON("closed", 42, "feature/x", "closedsha", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	app := makeGitApp("my-app", "pj-default", "https://github.com/org/repo", "main")
	app.Spec.Source.ProviderRef = "provider-a"
	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: mortisev1alpha1.ProjectSpec{
			Environments: []mortisev1alpha1.ProjectEnvironment{
				{Name: "production"},
				{Name: "staging"},
			},
			Preview: &mortisev1alpha1.PreviewConfig{Enabled: false},
		},
	}
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"default": project},
	}
	h := newTestHandler(kr)

	req := httptest.NewRequest(http.MethodPost, "/provider-a", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	// Close always attempts delete regardless of preview enabled state.
	if len(kr.deletedKeys) != 1 {
		t.Fatalf("expected 1 preview delete, got %d", len(kr.deletedKeys))
	}
	if kr.deletedKeys[0] != "pj-default/preview-pr-42" {
		t.Fatalf("deleted preview = %q, want pj-default/preview-pr-42", kr.deletedKeys[0])
	}
}

func TestPREvent_SourceEnvironmentFromProjectConfig(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	body := githubPRPayloadJSON("opened", 99, "br", "sha99", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	app := makeGitApp("web", "pj-default", "https://github.com/org/repo", "main")
	// Project has explicit SourceEnvironment override.
	proj := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: mortisev1alpha1.ProjectSpec{
			Environments: []mortisev1alpha1.ProjectEnvironment{
				{Name: "production"},
				{Name: "staging"},
				{Name: "dev"},
			},
			Preview: &mortisev1alpha1.PreviewConfig{
				Enabled:           true,
				SourceEnvironment: "dev",
			},
		},
	}
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"default": proj},
	}
	h := newTestHandler(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.createdPreviews) != 1 {
		t.Fatalf("expected 1 PE, got %d", len(kr.createdPreviews))
	}
	if got, want := kr.createdPreviews[0].Spec.SourceEnv, "dev"; got != want {
		t.Errorf("sourceEnv mismatch: got %q, want %q", got, want)
	}
}

func TestPREvent_StagingPreferred(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	body := githubPRPayloadJSON("opened", 8, "br", "sha8", "org/repo")

	app, proj := makePreviewGitApp("svc", "pj-default", "https://github.com/org/repo", "main", "", "")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"default": proj},
	}
	h := newTestHandler(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.createdPreviews) != 1 {
		t.Fatalf("expected 1 PE, got %d", len(kr.createdPreviews))
	}
	pe := kr.createdPreviews[0]
	// Project has both production and staging — staging is preferred.
	if pe.Spec.SourceEnv != "staging" {
		t.Errorf("expected sourceEnv=staging, got %q", pe.Spec.SourceEnv)
	}
}

func TestGitHubPREvent_Synchronize_UpdatesExistingPE(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	body := githubPRPayloadJSON("synchronize", 42, "feature/x", "newsha", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	app, proj := makePreviewGitApp("my-app", "pj-default", "https://github.com/org/repo", "main", "", "")
	existing := &mortisev1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "preview-pr-42",
			Namespace: "pj-default",
		},
		Spec: mortisev1alpha1.PreviewEnvironmentSpec{
			ProjectRef: "default",
			SourceEnv:  "staging",
			PullRequest: mortisev1alpha1.PullRequestRef{
				Number: 42,
				Branch: "feature/x",
				SHA:    "oldsha",
			},
		},
	}
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"default": proj},
		previewEnvs: map[string]*mortisev1alpha1.PreviewEnvironment{
			"pj-default/preview-pr-42": existing,
		},
	}
	h := newTestHandler(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.createdPreviews) != 0 {
		t.Errorf("expected no PE created on synchronize, got %d", len(kr.createdPreviews))
	}
	if len(kr.updatedPreviews) != 1 {
		t.Fatalf("expected 1 PE updated, got %d", len(kr.updatedPreviews))
	}
	if got := kr.updatedPreviews[0].Spec.PullRequest.SHA; got != "newsha" {
		t.Errorf("expected SHA updated to newsha, got %q", got)
	}
}

func TestGitHubPREvent_Synchronize_NoExistingPE_Creates(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	body := githubPRPayloadJSON("synchronize", 42, "feature/x", "sync-sha", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	app, proj := makePreviewGitApp("my-app", "pj-default", "https://github.com/org/repo", "main", "", "")
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"default": proj},
		// No existing PE.
	}
	h := newTestHandler(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.createdPreviews) != 1 {
		t.Fatalf("expected 1 PE created (idempotent sync), got %d", len(kr.createdPreviews))
	}
	if got := kr.createdPreviews[0].Spec.PullRequest.SHA; got != "sync-sha" {
		t.Errorf("expected SHA sync-sha, got %q", got)
	}
}

func TestGitHubPREvent_Closed_DeletesPE(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	body := githubPRPayloadJSON("closed", 42, "feature/x", "anysha", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	app, proj := makePreviewGitApp("my-app", "pj-default", "https://github.com/org/repo", "main", "", "")
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"default": proj},
	}
	h := newTestHandler(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.deletedKeys) != 1 {
		t.Fatalf("expected 1 PE deleted, got %d", len(kr.deletedKeys))
	}
	if kr.deletedKeys[0] != "pj-default/preview-pr-42" {
		t.Errorf("wrong PE deleted: %q", kr.deletedKeys[0])
	}
}

func TestGitHubPREvent_Closed_DeletesPE_Idempotent(t *testing.T) {
	// Close event always attempts delete by name — even if the PE doesn't
	// exist the delete is idempotent (no error).
	const secret = "whsec"
	body := githubPRPayloadJSON("closed", 42, "feature/x", "anysha", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	app, project := makePreviewGitApp("preview-app", "pj-proj", "https://github.com/org/repo", "main", "", "")

	kr := &fakeK8sReader{
		provider: gp,
		secrets: map[string]string{
			"mortise-system/wh-secret/value": secret,
		},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"proj": project},
	}
	h := newTestHandler(kr)

	req := httptest.NewRequest(http.MethodPost, "/github", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))
	req.Header.Set("X-GitHub-Event", "pull_request")

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.deletedKeys) != 1 {
		t.Fatalf("expected 1 delete attempt on closed event, got %d", len(kr.deletedKeys))
	}
	if kr.deletedKeys[0] != "pj-proj/preview-pr-42" {
		t.Errorf("wrong delete key: %q", kr.deletedKeys[0])
	}
}

func TestGiteaPREvent_Closed_DeletesPE(t *testing.T) {
	const secret = "giteaprsecret"
	const providerName = "gitea-homelab"

	body := githubPRPayloadJSON("closed", 9, "br", "sha", "user/myrepo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitea, "mortise-system", "wh-secret", "value")
	gp.Spec.Host = "https://gitea.example.com"
	app, proj := makePreviewGitApp("myrepo-app", "pj-gitea", "https://gitea.example.com/user/myrepo", "main", "", "")
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"gitea": proj},
	}
	h := newTestHandler(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitea-Event", "pull_request")
	req.Header.Set("X-Gitea-Signature", giteaSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.deletedKeys) != 1 {
		t.Fatalf("expected 1 PE deleted, got %d", len(kr.deletedKeys))
	}
	if kr.deletedKeys[0] != "pj-gitea/preview-pr-9" {
		t.Errorf("wrong delete key: %q", kr.deletedKeys[0])
	}
}

func TestGitLabPREvent_Closed_DeletesPE(t *testing.T) {
	const secret = "gitlabprsecret"
	const providerName = "gitlab-com"

	tests := []struct {
		name   string
		action string
		state  string
	}{
		{name: "close action", action: "close", state: "closed"},
		{name: "merge action", action: "merge", state: "merged"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := gitlabMRPayloadJSON(tc.action, tc.state, 17, "br", "sha17", "ns/project")

			gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitLab, "mortise-system", "wh-secret", "value")
			gp.Spec.Host = "https://gitlab.com"
			app, proj := makePreviewGitApp("gl-app", "pj-gl", "https://gitlab.com/ns/project", "main", "", "")
			kr := &fakeK8sReader{
				provider: gp,
				secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
				apps:     []mortisev1alpha1.App{app},
				projects: map[string]*mortisev1alpha1.Project{"gl": proj},
			}
			h := newTestHandler(kr)

			req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
			req.Header.Set("X-Gitlab-Token", secret)

			rr := httptest.NewRecorder()
			h.Routes().ServeHTTP(rr, req)

			if rr.Code != http.StatusAccepted {
				t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
			}
			if len(kr.deletedKeys) != 1 {
				t.Fatalf("expected 1 PE deleted, got %d", len(kr.deletedKeys))
			}
			if kr.deletedKeys[0] != "pj-gl/preview-pr-17" {
				t.Errorf("wrong delete key: %q", kr.deletedKeys[0])
			}
		})
	}
}

func TestPREvent_Closed_NoExistingPE_Idempotent(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	body := githubPRPayloadJSON("closed", 42, "br", "sha", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	app, proj := makePreviewGitApp("my-app", "pj-default", "https://github.com/org/repo", "main", "", "")
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"default": proj},
		// No existing PE — delete is idempotent.
	}
	h := newTestHandler(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	// Delete is always attempted (idempotent), so we expect one delete key.
	if len(kr.deletedKeys) != 1 {
		t.Errorf("expected 1 delete attempt, got %d", len(kr.deletedKeys))
	}
	if len(kr.createdPreviews) != 0 {
		t.Errorf("expected no PE created on close, got %d", len(kr.createdPreviews))
	}
}

func TestGitHubPREvent_InvalidSignature_Unauthorized(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	body := githubPRPayloadJSON("opened", 1, "br", "sha", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	app, proj := makePreviewGitApp("my-app", "pj-default", "https://github.com/org/repo", "main", "", "")
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"default": proj},
	}
	h := New(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", "sha256=bogus")

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	if len(kr.createdPreviews) != 0 {
		t.Errorf("expected no PE created on invalid signature, got %d", len(kr.createdPreviews))
	}
}

func TestGiteaPREvent_InvalidSignature_Unauthorized(t *testing.T) {
	const secret = "prsecret"
	const providerName = "gitea-homelab"

	body := githubPRPayloadJSON("opened", 1, "br", "sha", "user/myrepo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitea, "mortise-system", "wh-secret", "value")
	gp.Spec.Host = "https://gitea.example.com"
	app, proj := makePreviewGitApp("myrepo-app", "pj-gitea", "https://gitea.example.com/user/myrepo", "main", "", "")
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"gitea": proj},
	}
	h := New(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitea-Event", "pull_request")
	req.Header.Set("X-Gitea-Signature", "not-a-real-hmac")

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	if len(kr.createdPreviews) != 0 {
		t.Errorf("expected no PE created on invalid signature, got %d", len(kr.createdPreviews))
	}
}

func TestGitLabPREvent_InvalidToken_Unauthorized(t *testing.T) {
	const secret = "gitlabprsecret"
	const providerName = "gitlab-com"

	body := gitlabMRPayloadJSON("open", "opened", 1, "br", "sha", "ns/project")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitLab, "mortise-system", "wh-secret", "value")
	gp.Spec.Host = "https://gitlab.com"
	app, proj := makePreviewGitApp("gl-app", "pj-gl", "https://gitlab.com/ns/project", "main", "", "")
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"gl": proj},
	}
	h := New(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
	req.Header.Set("X-Gitlab-Token", "wrong-token")

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	if len(kr.createdPreviews) != 0 {
		t.Errorf("expected no PE created on invalid token, got %d", len(kr.createdPreviews))
	}
}

func TestPREvent_ProjectOnlyProductionEnv_NoPECreated(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	body := githubPRPayloadJSON("opened", 12, "br", "sha12", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	app := makeGitApp("my-app", "pj-default", "https://github.com/org/repo", "main")
	// Project has preview enabled but only production env — no non-prod env to inherit from.
	proj := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: mortisev1alpha1.ProjectSpec{
			Environments: []mortisev1alpha1.ProjectEnvironment{{Name: "production"}},
			Preview: &mortisev1alpha1.PreviewConfig{
				Enabled: true,
			},
		},
	}
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"default": proj},
	}
	h := newTestHandler(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.createdPreviews) != 0 {
		t.Errorf("expected no PE created when project has only production env, got %d", len(kr.createdPreviews))
	}
}

func TestPREvent_NoMatchingRepo_NoPECreated(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	body := githubPRPayloadJSON("opened", 1, "br", "sha", "org/unrelated")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	app, proj := makePreviewGitApp("my-app", "pj-default", "https://github.com/org/repo", "main", "", "")
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"default": proj},
	}
	h := newTestHandler(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.createdPreviews) != 0 {
		t.Errorf("expected no PE created for non-matching repo, got %d", len(kr.createdPreviews))
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
		// Different owner, same repo name — no match (prevents cross-owner spoofing).
		{"https://github.com/org/repo", "attacker/repo", false},
		// Bare repo name without owner — no match.
		{"https://github.com/org/repo", "repo", false},
	}
	for _, tc := range tests {
		got := repoMatches(tc.a, tc.b)
		if got != tc.match {
			t.Errorf("repoMatches(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.match)
		}
	}
}

// TestWebhook_EmptyProviderRefPolicy pins the empty-providerRef matching
// decision: an empty ref matches only when exactly one GitProvider is
// registered (the delivery necessarily came from it); with several
// registered, empty-ref apps are skipped instead of matching webhooks from
// ANY provider. A non-empty ref must equal the delivering provider.
func TestWebhook_EmptyProviderRefPolicy(t *testing.T) {
	const secret = "secret"

	emptyRef := makeGitApp("app-empty", "pj-a", "https://github.com/org/repo", "main")
	matchingRef := makeGitApp("app-match", "pj-a", "https://github.com/org/repo", "main")
	matchingRef.Spec.Source.ProviderRef = "github-main"
	otherRef := makeGitApp("app-other", "pj-a", "https://github.com/org/repo", "main")
	otherRef.Spec.Source.ProviderRef = "gitlab-main"

	tests := []struct {
		name          string
		providerCount int
		wantPatched   []string // ns/name expected patched
	}{
		{
			name:          "single provider: empty ref matches, foreign ref does not",
			providerCount: 1,
			wantPatched:   []string{"pj-a/app-empty", "pj-a/app-match"},
		},
		{
			name:          "multiple providers: empty ref is ambiguous and skipped",
			providerCount: 2,
			wantPatched:   []string{"pj-a/app-match"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := pushPayloadJSON("refs/heads/main", "shaaaaa", "org/repo")
			gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
			kr := &fakeK8sReader{
				provider:      gp,
				providerCount: tc.providerCount,
				secrets:       map[string]string{"mortise-system/wh-secret/value": secret},
				apps:          []mortisev1alpha1.App{emptyRef, matchingRef, otherRef},
			}
			h := newTestHandler(kr)

			req := httptest.NewRequest(http.MethodPost, "/github-main", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

			rr := httptest.NewRecorder()
			h.Routes().ServeHTTP(rr, req)
			if rr.Code != http.StatusAccepted {
				t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
			}

			if len(kr.patched) != len(tc.wantPatched) {
				t.Fatalf("expected %d patched apps, got %v", len(tc.wantPatched), kr.patched)
			}
			for _, key := range tc.wantPatched {
				if kr.patched[key] != "shaaaaa" {
					t.Errorf("expected %s patched, got %v", key, kr.patched)
				}
			}
		})
	}
}

// A failed HMAC verification is recorded on the GitProvider, once per
// provider per minute regardless of how many deliveries fail (CAI-262).
func TestInvalidSignatureIsRecordedOnTheProvider(t *testing.T) {
	kr := &fakeK8sReader{
		provider: makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh", "secret"),
		secrets:  map[string]string{"mortise-system/wh/secret": "right"},
	}
	h := New(kr) // real signature verification, unlike newTestHandler
	fc := testclock.NewFakeClock(time.Date(2026, 8, 30, 4, 0, 0, 0, time.UTC))
	h.Clock = fc

	send := func() int {
		req := httptest.NewRequest(http.MethodPost, "/github", bytes.NewReader([]byte(`{}`)))
		req.Header.Set("X-GitHub-Event", "push")
		req.Header.Set("X-Hub-Signature-256", "sha256=invalidsignature")
		rr := httptest.NewRecorder()
		h.Routes().ServeHTTP(rr, req)
		return rr.Code
	}
	for i := 0; i < 3; i++ {
		if code := send(); code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", code)
		}
	}
	if got := kr.signatureWrites; len(got) != 1 || got[0] != "github=true" {
		t.Fatalf("three mismatches within a minute must write once: %v", got)
	}
	fc.Step(2 * time.Minute)
	send()
	if got := kr.signatureWrites; len(got) != 2 {
		t.Fatalf("a mismatch after the interval must write again: %v", got)
	}
}
