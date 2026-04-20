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

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fakeK8sReader is a test double for k8sReader.
type fakeK8sReader struct {
	provider *mortisev1alpha1.GitProvider
	secrets  map[string]string // "ns/name/key" -> value
	apps     []mortisev1alpha1.App
	projects map[string]*mortisev1alpha1.Project // name -> project
	err      error

	// patched records calls to patchAppRevision: app namespace/name -> sha
	patched map[string]string

	// preview environment tracking
	previewEnvs     []mortisev1alpha1.PreviewEnvironment
	createdPreviews []mortisev1alpha1.PreviewEnvironment
	updatedPreviews []mortisev1alpha1.PreviewEnvironment
	deletedPreviews []mortisev1alpha1.PreviewEnvironment
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

func (f *fakeK8sReader) listPreviewEnvironments(_ context.Context, _ string) ([]mortisev1alpha1.PreviewEnvironment, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.previewEnvs, nil
}

func (f *fakeK8sReader) createPreviewEnvironment(_ context.Context, pe *mortisev1alpha1.PreviewEnvironment) error {
	f.createdPreviews = append(f.createdPreviews, *pe)
	return nil
}

func (f *fakeK8sReader) updatePreviewEnvironment(_ context.Context, pe *mortisev1alpha1.PreviewEnvironment) error {
	f.updatedPreviews = append(f.updatedPreviews, *pe)
	return nil
}

func (f *fakeK8sReader) deletePreviewEnvironment(_ context.Context, pe *mortisev1alpha1.PreviewEnvironment) error {
	f.deletedPreviews = append(f.deletedPreviews, *pe)
	return nil
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
func makePreviewGitApp(name, ns, repo, branch string, domain, ttl string) (mortisev1alpha1.App, *mortisev1alpha1.Project) {
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
				Domain:  domain,
				TTL:     ttl,
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
	app, proj := makePreviewGitApp("my-app", "pj-default", "https://github.com/org/repo", "main", "pr-{number}.{app}.example.com", "24h")
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
	if pe.Name != "my-app-preview-pr-42" {
		t.Errorf("unexpected PE name: %q", pe.Name)
	}
	if pe.Namespace != "pj-default" {
		t.Errorf("unexpected PE namespace: %q", pe.Namespace)
	}
	if pe.Spec.AppRef != "my-app" {
		t.Errorf("appRef mismatch: %q", pe.Spec.AppRef)
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
	if pe.Spec.Domain != "pr-42.my-app.example.com" {
		t.Errorf("domain template not resolved: %q", pe.Spec.Domain)
	}
	if pe.Spec.TTL.Duration.Hours() != 24 {
		t.Errorf("ttl mismatch: %v", pe.Spec.TTL.Duration)
	}
}

func TestGiteaPREvent_Opened_CreatesPreviewEnvironment(t *testing.T) {
	const secret = "giteaprsecret"
	const providerName = "gitea-homelab"

	body := githubPRPayloadJSON("opened", 7, "topic/feat", "gitasha", "user/myrepo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitea, "mortise-system", "wh-secret", "value")
	gp.Spec.Host = "https://gitea.example.com"
	app, proj := makePreviewGitApp("myrepo-app", "pj-gitea", "https://gitea.example.com/user/myrepo", "main", "pr-{number}.example.com", "")
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
	if pe.Name != "myrepo-app-preview-pr-7" {
		t.Errorf("unexpected PE name: %q", pe.Name)
	}
	if pe.Spec.PullRequest.Number != 7 || pe.Spec.PullRequest.SHA != "gitasha" {
		t.Errorf("PR ref mismatch: %+v", pe.Spec.PullRequest)
	}
	// Default TTL is 72h when unset.
	if pe.Spec.TTL.Duration.Hours() != 72 {
		t.Errorf("expected default 72h TTL, got %v", pe.Spec.TTL.Duration)
	}
}

func TestGitLabPREvent_Opened_CreatesPreviewEnvironment(t *testing.T) {
	const secret = "gitlabprsecret"
	const providerName = "gitlab-com"

	body := gitlabMRPayloadJSON("open", "opened", 11, "feat/branch", "mrsha1", "ns/project")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitLab, "mortise-system", "wh-secret", "value")
	gp.Spec.Host = "https://gitlab.com"
	app, proj := makePreviewGitApp("gl-app", "pj-gl", "https://gitlab.com/ns/project", "main", "pr-{number}.{app}.gl.example.com", "")
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
	if pe.Name != "gl-app-preview-pr-11" {
		t.Errorf("unexpected PE name: %q", pe.Name)
	}
	if pe.Spec.PullRequest.Number != 11 || pe.Spec.PullRequest.Branch != "feat/branch" || pe.Spec.PullRequest.SHA != "mrsha1" {
		t.Errorf("PR ref mismatch: %+v", pe.Spec.PullRequest)
	}
	if pe.Spec.Domain != "pr-11.gl-app.gl.example.com" {
		t.Errorf("domain mismatch: %q", pe.Spec.Domain)
	}
}

func TestPREvent_ProjectPreviewDisabled_NoPECreated(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	body := githubPRPayloadJSON("opened", 5, "f", "sha", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	app := makeGitApp("no-preview", "pj-default", "https://github.com/org/repo", "main")
	// preview nil → disabled.
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
	}
	h := New(kr)

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
	proj2 := makeProject("default", &mortisev1alpha1.PreviewConfig{Enabled: false, Domain: "pr-{number}.example.com"})
	kr2 := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app2},
		projects: map[string]*mortisev1alpha1.Project{"default": proj2},
	}
	h2 := New(kr2)

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

func TestPREvent_DomainTemplate_Resolved(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	body := githubPRPayloadJSON("opened", 99, "br", "sha99", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	app, proj := makePreviewGitApp("web", "pj-default", "https://github.com/org/repo", "main", "pr-{number}-{app}.preview.example.com", "")
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
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.createdPreviews) != 1 {
		t.Fatalf("expected 1 PE, got %d", len(kr.createdPreviews))
	}
	if got, want := kr.createdPreviews[0].Spec.Domain, "pr-99-web.preview.example.com"; got != want {
		t.Errorf("domain template mismatch: got %q, want %q", got, want)
	}
}

func TestPREvent_StagingInheritance(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	body := githubPRPayloadJSON("opened", 8, "br", "sha8", "org/repo")

	replicas := int32(2)
	app, proj := makePreviewGitApp("svc", "pj-default", "https://github.com/org/repo", "main", "pr-{number}.example.com", "")
	app.Spec.Environments = []mortisev1alpha1.Environment{
		{
			Name:     "production",
			Replicas: func() *int32 { r := int32(5); return &r }(),
		},
		{
			Name:      "staging",
			Replicas:  &replicas,
			Resources: mortisev1alpha1.ResourceRequirements{CPU: "250m", Memory: "128Mi"},
			Env: []mortisev1alpha1.EnvVar{
				{Name: "LOG_LEVEL", Value: "debug"},
			},
			Bindings: []mortisev1alpha1.Binding{
				{Ref: "my-db"},
			},
		},
	}

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
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
	if pe.Spec.Replicas == nil || *pe.Spec.Replicas != 2 {
		t.Errorf("replicas not inherited from staging: %v", pe.Spec.Replicas)
	}
	if pe.Spec.Resources.CPU != "250m" || pe.Spec.Resources.Memory != "128Mi" {
		t.Errorf("resources not inherited from staging: %+v", pe.Spec.Resources)
	}
	if len(pe.Spec.Env) != 1 || pe.Spec.Env[0].Name != "LOG_LEVEL" {
		t.Errorf("env not inherited from staging: %+v", pe.Spec.Env)
	}
	if len(pe.Spec.Bindings) != 1 || pe.Spec.Bindings[0].Ref != "my-db" {
		t.Errorf("bindings not inherited from staging: %+v", pe.Spec.Bindings)
	}
}

func TestPREvent_PreviewResourcesOverride(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	body := githubPRPayloadJSON("opened", 3, "br", "sha3", "org/repo")

	replicas := int32(2)
	app, proj := makePreviewGitApp("svc", "pj-default", "https://github.com/org/repo", "main", "pr-{number}.example.com", "12h")
	app.Spec.Environments = []mortisev1alpha1.Environment{
		{
			Name:      "staging",
			Replicas:  &replicas,
			Resources: mortisev1alpha1.ResourceRequirements{CPU: "500m", Memory: "256Mi"},
		},
	}
	// Preview-level resource override (set on the Project's preview config).
	proj.Spec.Preview.Resources = mortisev1alpha1.ResourceRequirements{CPU: "100m", Memory: "64Mi"}

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
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
	if pe.Spec.Resources.CPU != "100m" || pe.Spec.Resources.Memory != "64Mi" {
		t.Errorf("expected preview resources override to win, got %+v", pe.Spec.Resources)
	}
	if pe.Spec.TTL.Duration.Hours() != 12 {
		t.Errorf("ttl override mismatch: %v", pe.Spec.TTL.Duration)
	}
}

func TestGitHubPREvent_Synchronize_UpdatesExistingPE(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	body := githubPRPayloadJSON("synchronize", 42, "feature/x", "newsha", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	app, proj := makePreviewGitApp("my-app", "pj-default", "https://github.com/org/repo", "main", "pr-{number}.example.com", "")
	existing := mortisev1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-app-preview-pr-42",
			Namespace: "pj-default",
		},
		Spec: mortisev1alpha1.PreviewEnvironmentSpec{
			AppRef: "my-app",
			PullRequest: mortisev1alpha1.PullRequestRef{
				Number: 42,
				Branch: "feature/x",
				SHA:    "oldsha",
			},
		},
	}
	kr := &fakeK8sReader{
		provider:    gp,
		secrets:     map[string]string{"mortise-system/wh-secret/value": secret},
		apps:        []mortisev1alpha1.App{app},
		projects:    map[string]*mortisev1alpha1.Project{"default": proj},
		previewEnvs: []mortisev1alpha1.PreviewEnvironment{existing},
	}
	h := New(kr)

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
	app, proj := makePreviewGitApp("my-app", "pj-default", "https://github.com/org/repo", "main", "pr-{number}.example.com", "")
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"default": proj},
		// No existing PE.
	}
	h := New(kr)

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
	app, proj := makePreviewGitApp("my-app", "pj-default", "https://github.com/org/repo", "main", "pr-{number}.example.com", "")
	existing := mortisev1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-app-preview-pr-42",
			Namespace: "pj-default",
		},
	}
	kr := &fakeK8sReader{
		provider:    gp,
		secrets:     map[string]string{"mortise-system/wh-secret/value": secret},
		apps:        []mortisev1alpha1.App{app},
		projects:    map[string]*mortisev1alpha1.Project{"default": proj},
		previewEnvs: []mortisev1alpha1.PreviewEnvironment{existing},
	}
	h := New(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.deletedPreviews) != 1 {
		t.Fatalf("expected 1 PE deleted, got %d", len(kr.deletedPreviews))
	}
	if kr.deletedPreviews[0].Name != "my-app-preview-pr-42" {
		t.Errorf("wrong PE deleted: %q", kr.deletedPreviews[0].Name)
	}
}

func TestGiteaPREvent_Closed_DeletesPE(t *testing.T) {
	const secret = "giteaprsecret"
	const providerName = "gitea-homelab"

	body := githubPRPayloadJSON("closed", 9, "br", "sha", "user/myrepo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitea, "mortise-system", "wh-secret", "value")
	gp.Spec.Host = "https://gitea.example.com"
	app, proj := makePreviewGitApp("myrepo-app", "pj-gitea", "https://gitea.example.com/user/myrepo", "main", "pr-{number}.example.com", "")
	existing := mortisev1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "myrepo-app-preview-pr-9",
			Namespace: "pj-gitea",
		},
	}
	kr := &fakeK8sReader{
		provider:    gp,
		secrets:     map[string]string{"mortise-system/wh-secret/value": secret},
		apps:        []mortisev1alpha1.App{app},
		projects:    map[string]*mortisev1alpha1.Project{"gitea": proj},
		previewEnvs: []mortisev1alpha1.PreviewEnvironment{existing},
	}
	h := New(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitea-Event", "pull_request")
	req.Header.Set("X-Gitea-Signature", giteaSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.deletedPreviews) != 1 {
		t.Fatalf("expected 1 PE deleted, got %d", len(kr.deletedPreviews))
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
			app, proj := makePreviewGitApp("gl-app", "pj-gl", "https://gitlab.com/ns/project", "main", "pr-{number}.example.com", "")
			existing := mortisev1alpha1.PreviewEnvironment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "gl-app-preview-pr-17",
					Namespace: "pj-gl",
				},
			}
			kr := &fakeK8sReader{
				provider:    gp,
				secrets:     map[string]string{"mortise-system/wh-secret/value": secret},
				apps:        []mortisev1alpha1.App{app},
				projects:    map[string]*mortisev1alpha1.Project{"gl": proj},
				previewEnvs: []mortisev1alpha1.PreviewEnvironment{existing},
			}
			h := New(kr)

			req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
			req.Header.Set("X-Gitlab-Token", secret)

			rr := httptest.NewRecorder()
			h.Routes().ServeHTTP(rr, req)

			if rr.Code != http.StatusAccepted {
				t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
			}
			if len(kr.deletedPreviews) != 1 {
				t.Fatalf("expected 1 PE deleted, got %d", len(kr.deletedPreviews))
			}
		})
	}
}

func TestPREvent_Closed_NoExistingPE_Idempotent(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	body := githubPRPayloadJSON("closed", 42, "br", "sha", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	app, proj := makePreviewGitApp("my-app", "pj-default", "https://github.com/org/repo", "main", "pr-{number}.example.com", "")
	kr := &fakeK8sReader{
		provider: gp,
		secrets:  map[string]string{"mortise-system/wh-secret/value": secret},
		apps:     []mortisev1alpha1.App{app},
		projects: map[string]*mortisev1alpha1.Project{"default": proj},
		// No existing PE.
	}
	h := New(kr)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.deletedPreviews) != 0 {
		t.Errorf("expected no PE deleted (none existed), got %d", len(kr.deletedPreviews))
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
	app, proj := makePreviewGitApp("my-app", "pj-default", "https://github.com/org/repo", "main", "pr-{number}.example.com", "")
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
	app, proj := makePreviewGitApp("myrepo-app", "pj-gitea", "https://gitea.example.com/user/myrepo", "main", "pr-{number}.example.com", "")
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
	app, proj := makePreviewGitApp("gl-app", "pj-gl", "https://gitlab.com/ns/project", "main", "pr-{number}.example.com", "")
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

func TestPREvent_ProjectMissingStagingEnv_NoPECreated(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	body := githubPRPayloadJSON("opened", 12, "br", "sha12", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	app := makeGitApp("my-app", "pj-default", "https://github.com/org/repo", "main")
	// Project has preview enabled but no staging env declared.
	proj := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: mortisev1alpha1.ProjectSpec{
			Environments: []mortisev1alpha1.ProjectEnvironment{{Name: "production"}},
			Preview: &mortisev1alpha1.PreviewConfig{
				Enabled: true,
				Domain:  "pr-{number}.example.com",
			},
		},
	}
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
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kr.createdPreviews) != 0 {
		t.Errorf("expected no PE created when project lacks staging env, got %d", len(kr.createdPreviews))
	}
}

func TestPREvent_NoMatchingRepo_NoPECreated(t *testing.T) {
	const secret = "prsecret"
	const providerName = "github-main"

	body := githubPRPayloadJSON("opened", 1, "br", "sha", "org/unrelated")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	app, proj := makePreviewGitApp("my-app", "pj-default", "https://github.com/org/repo", "main", "pr-{number}.example.com", "")
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
	}
	for _, tc := range tests {
		got := repoMatches(tc.a, tc.b)
		if got != tc.match {
			t.Errorf("repoMatches(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.match)
		}
	}
}
