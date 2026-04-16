package git

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

// fakeGitAPI is a test double satisfying GitAPI.
type fakeGitAPI struct {
	webhookErr error
	statusErr  error
	sigErr     error
	creds      GitCredentials
	credsErr   error
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
		t    mortisev1alpha1.GitProviderType
		host string
	}{
		{mortisev1alpha1.GitProviderTypeGitHub, "https://github.com"},
		{mortisev1alpha1.GitProviderTypeGitLab, "https://gitlab.com"},
		{mortisev1alpha1.GitProviderTypeGitea, "https://gitea.example.com"},
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
		}
	}
}
