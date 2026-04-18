package git

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"testing"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

// fakeGitAPI is a test double satisfying GitAPI.
type fakeGitAPI struct {
	webhookErr  error
	statusErr   error
	sigErr      error
	creds       GitCredentials
	credsErr    error
	repos       []Repository
	reposErr    error
	branches    []Branch
	branchesErr error
}

func (f *fakeGitAPI) RegisterWebhook(_ context.Context, _ string, _ WebhookConfig) error {
	return f.webhookErr
}

func (f *fakeGitAPI) PostCommitStatus(_ context.Context, _, _ string, _ CommitStatus) error {
	return f.statusErr
}

func (f *fakeGitAPI) VerifyWebhookSignature(_ []byte, _ http.Header) error {
	return f.sigErr
}

func (f *fakeGitAPI) ResolveCloneCredentials(_ context.Context, _ string) (GitCredentials, error) {
	return f.creds, f.credsErr
}

func (f *fakeGitAPI) ListRepos(_ context.Context) ([]Repository, error) {
	return f.repos, f.reposErr
}

func (f *fakeGitAPI) ListBranches(_ context.Context, _ string) ([]Branch, error) {
	return f.branches, f.branchesErr
}

func (f *fakeGitAPI) ListTree(_ context.Context, _, _, _, _ string) ([]TreeEntry, error) {
	return nil, nil
}

var _ GitAPI = (*fakeGitAPI)(nil)

// TestFakeGitAPI verifies the fakeGitAPI satisfies the interface and returns configured values.
func TestFakeGitAPI(t *testing.T) {
	fake := &fakeGitAPI{creds: GitCredentials{Token: "tok"}}
	creds, err := fake.ResolveCloneCredentials(context.Background(), "org/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.Token != "tok" {
		t.Errorf("expected token tok, got %q", creds.Token)
	}
}

// TestGitHubVerifySignature tests HMAC-SHA256 verification without a real HTTP server.
func TestGitHubVerifySignature(t *testing.T) {
	const secret = "test-secret"
	api := &GitHubAPI{secret: secret}

	body := []byte(`{"ref":"refs/heads/main"}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	hdr := http.Header{"X-Hub-Signature-256": []string{sig}}
	if err := api.VerifyWebhookSignature(body, hdr); err != nil {
		t.Errorf("expected valid signature, got: %v", err)
	}

	badHdr := http.Header{"X-Hub-Signature-256": []string{"sha256=badsignature"}}
	if err := api.VerifyWebhookSignature(body, badHdr); err == nil {
		t.Error("expected signature mismatch error")
	}

	emptyHdr := http.Header{}
	if err := api.VerifyWebhookSignature(body, emptyHdr); err == nil {
		t.Error("expected missing header error")
	}
}

// TestGiteaVerifySignature tests HMAC-SHA256 verification for Gitea.
func TestGiteaVerifySignature(t *testing.T) {
	const secret = "gitea-secret"
	api := &GiteaAPI{secret: secret}

	body := []byte(`{"ref":"refs/heads/main"}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	hdr := http.Header{"X-Gitea-Signature": []string{sig}}
	if err := api.VerifyWebhookSignature(body, hdr); err != nil {
		t.Errorf("expected valid signature, got: %v", err)
	}

	emptyHdr := http.Header{}
	if err := api.VerifyWebhookSignature(body, emptyHdr); err == nil {
		t.Error("expected missing header error")
	}
}

// TestGitLabVerifyToken tests constant-time token comparison for GitLab.
func TestGitLabVerifyToken(t *testing.T) {
	const secret = "gitlab-token"
	api := &GitLabAPI{secret: secret}

	hdr := http.Header{"X-Gitlab-Token": []string{secret}}
	if err := api.VerifyWebhookSignature(nil, hdr); err != nil {
		t.Errorf("expected valid token, got: %v", err)
	}

	badHdr := http.Header{"X-Gitlab-Token": []string{"wrong"}}
	if err := api.VerifyWebhookSignature(nil, badHdr); err == nil {
		t.Error("expected token mismatch error")
	}

	emptyHdr := http.Header{}
	if err := api.VerifyWebhookSignature(nil, emptyHdr); err == nil {
		t.Error("expected missing header error")
	}
}

// TestSplitRepo verifies the repo-name parsing helper.
func TestSplitRepo(t *testing.T) {
	tests := []struct {
		input       string
		wantOwner   string
		wantRepo    string
		expectError bool
	}{
		{"owner/repo", "owner", "repo", false},
		{"github.com/owner/repo", "owner", "repo", false},
		{"https://github.com/owner/repo", "owner", "repo", false},
		{"only-one-part", "", "", true},
	}
	for _, tt := range tests {
		owner, repo, err := splitRepo(tt.input)
		if tt.expectError {
			if err == nil {
				t.Errorf("splitRepo(%q): expected error, got owner=%q repo=%q", tt.input, owner, repo)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitRepo(%q): unexpected error: %v", tt.input, err)
			continue
		}
		if owner != tt.wantOwner || repo != tt.wantRepo {
			t.Errorf("splitRepo(%q): got owner=%q repo=%q, want owner=%q repo=%q",
				tt.input, owner, repo, tt.wantOwner, tt.wantRepo)
		}
	}
}

// TestFactory verifies NewGitAPIFromProvider returns the right impl type.
func TestFactory(t *testing.T) {
	ref := mortisev1alpha1.SecretRef{Namespace: "ns", Name: "s", Key: "k"}
	for _, tt := range []struct {
		t        mortisev1alpha1.GitProviderType
		host     string
		wantType string
	}{
		{mortisev1alpha1.GitProviderTypeGitHub, "https://github.com", "*git.GitHubAPI"},
		{mortisev1alpha1.GitProviderTypeGitLab, "https://gitlab.com", "*git.GitLabAPI"},
		{mortisev1alpha1.GitProviderTypeGitea, "https://gitea.example.com", "*git.GiteaAPI"},
	} {
		gp := &mortisev1alpha1.GitProvider{
			Spec: mortisev1alpha1.GitProviderSpec{
				Type: tt.t,
				Host: tt.host,
				OAuth: mortisev1alpha1.OAuthConfig{
					ClientIDSecretRef:     ref,
					ClientSecretSecretRef: ref,
				},
				WebhookSecretRef: ref,
			},
		}
		api, err := NewGitAPIFromProvider(gp, "tok", "wh-secret")
		if err != nil {
			t.Errorf("NewGitAPIFromProvider(%s): unexpected error: %v", tt.t, err)
			continue
		}
		if api == nil {
			t.Errorf("NewGitAPIFromProvider(%s): got nil api", tt.t)
			continue
		}
		got := fmt.Sprintf("%T", api)
		if got != tt.wantType {
			t.Errorf("NewGitAPIFromProvider(%s): got type %s, want %s", tt.t, got, tt.wantType)
		}
	}
}

// TestFakeGitAPI_ListRepos verifies the fake returns configured repos.
func TestFakeGitAPI_ListRepos(t *testing.T) {
	fake := &fakeGitAPI{repos: []Repository{
		{FullName: "org/repo", Name: "repo", Private: true},
	}}
	repos, err := fake.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 1 || repos[0].FullName != "org/repo" {
		t.Errorf("unexpected repos: %+v", repos)
	}
}

// TestFakeGitAPI_ListBranches verifies the fake returns configured branches.
func TestFakeGitAPI_ListBranches(t *testing.T) {
	fake := &fakeGitAPI{branches: []Branch{
		{Name: "main", Default: true},
		{Name: "dev", Default: false},
	}}
	branches, err := fake.ListBranches(context.Background(), "org/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(branches) != 2 || branches[0].Name != "main" || !branches[0].Default {
		t.Errorf("unexpected branches: %+v", branches)
	}
}

// TestFactory_GitHubApp verifies that NewGitHubAppAPIFromProvider returns a
// GitHubAppAPI when mode=github-app.
func TestFactory_GitHubApp(t *testing.T) {
	pemBytes := generateTestPEM(t)
	gp := &mortisev1alpha1.GitProvider{
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitHub,
			Host: "https://github.com",
			Mode: "github-app",
			GitHubApp: &mortisev1alpha1.GitHubAppConfig{
				AppID:          123,
				Slug:           "mortise-test",
				InstallationID: 456,
			},
		},
	}
	api, err := NewGitHubAppAPIFromProvider(gp, pemBytes, "wh-secret")
	if err != nil {
		t.Fatalf("NewGitHubAppAPIFromProvider: %v", err)
	}
	got := fmt.Sprintf("%T", api)
	if got != "*git.GitHubAppAPI" {
		t.Errorf("got type %s, want *git.GitHubAppAPI", got)
	}
	// Verify the installation ID was set.
	ghApp := api.(*GitHubAppAPI)
	if ghApp.installationID != 456 {
		t.Errorf("installationID: got %d, want 456", ghApp.installationID)
	}
}

// TestFactory_GitHubApp_NoConfig verifies that NewGitHubAppAPIFromProvider
// returns an error when githubApp config is nil.
func TestFactory_GitHubApp_NoConfig(t *testing.T) {
	gp := &mortisev1alpha1.GitProvider{
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitHub,
			Host: "https://github.com",
			Mode: "github-app",
		},
	}
	_, err := NewGitHubAppAPIFromProvider(gp, nil, "")
	if err == nil {
		t.Error("expected error when githubApp is nil")
	}
}

// TestFactory_UnknownType verifies that an unsupported provider type returns an error.
func TestFactory_UnknownType(t *testing.T) {
	ref := mortisev1alpha1.SecretRef{Namespace: "ns", Name: "s", Key: "k"}
	gp := &mortisev1alpha1.GitProvider{
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderType("bitbucket"),
			Host: "https://bitbucket.org",
			OAuth: mortisev1alpha1.OAuthConfig{
				ClientIDSecretRef:     ref,
				ClientSecretSecretRef: ref,
			},
			WebhookSecretRef: ref,
		},
	}
	_, err := NewGitAPIFromProvider(gp, "tok", "wh-secret")
	if err == nil {
		t.Error("expected error for unsupported provider type")
	}
}
