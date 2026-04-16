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
)

// fakeK8sReader is a test double for k8sReader.
type fakeK8sReader struct {
	provider *mortisev1alpha1.GitProvider
	secrets  map[string]string // "ns/name/key" -> value
	err      error
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
	builds := make(chan BuildRequest, 1)

	body := pushPayloadJSON("refs/heads/main", "abc123def456", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	kr := &fakeK8sReader{
		provider: gp,
		secrets: map[string]string{
			"mortise-system/wh-secret/value": secret,
		},
	}
	h := New(kr, builds)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.SetPathValue("provider", providerName) // net/http 1.22+
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature-256", githubSignature(body, secret))
	req.Header.Set("X-Github-Event", "push")

	// Use the chi router so the provider param is resolved.
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}

	select {
	case br := <-builds:
		if br.Repo != "org/repo" {
			t.Errorf("expected repo org/repo, got %q", br.Repo)
		}
		if br.SHA != "abc123def456" {
			t.Errorf("expected sha abc123def456, got %q", br.SHA)
		}
	default:
		t.Fatal("expected build request to be enqueued")
	}
}

func TestGitHubWebhook_InvalidSignature(t *testing.T) {
	const secret = "mysecret"
	const providerName = "github-main"
	builds := make(chan BuildRequest, 1)

	body := pushPayloadJSON("refs/heads/main", "abc123def456", "org/repo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitHub, "mortise-system", "wh-secret", "value")
	kr := &fakeK8sReader{
		provider: gp,
		secrets: map[string]string{
			"mortise-system/wh-secret/value": secret,
		},
	}
	h := New(kr, builds)

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
	builds := make(chan BuildRequest, 1)

	body := pushPayloadJSON("refs/heads/feature", "deadbeef1234", "user/myrepo")

	gp := makeGitProvider(mortisev1alpha1.GitProviderTypeGitea, "mortise-system", "wh-secret", "value")
	gp.Spec.Host = "https://gitea.example.com"
	kr := &fakeK8sReader{
		provider: gp,
		secrets: map[string]string{
			"mortise-system/wh-secret/value": secret,
		},
	}
	h := New(kr, builds)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitea-Signature", giteaSignature(body, secret))

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}

	select {
	case br := <-builds:
		if br.Repo != "user/myrepo" {
			t.Errorf("expected repo user/myrepo, got %q", br.Repo)
		}
	default:
		t.Fatal("expected build request to be enqueued")
	}
}

func TestGitLabWebhook_ValidToken(t *testing.T) {
	const secret = "gitlab-webhook-token"
	const providerName = "gitlab-com"
	builds := make(chan BuildRequest, 1)

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
	}
	h := New(kr, builds)

	req := httptest.NewRequest(http.MethodPost, "/"+providerName, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Token", secret)

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}

	select {
	case br := <-builds:
		if br.SHA != "cafebabe5678" {
			t.Errorf("expected sha cafebabe5678, got %q", br.SHA)
		}
	default:
		t.Fatal("expected build request to be enqueued")
	}
}

func TestWebhook_ProviderNotFound(t *testing.T) {
	builds := make(chan BuildRequest, 1)
	kr := &fakeK8sReader{err: fmt.Errorf("not found")}
	h := New(kr, builds)

	req := httptest.NewRequest(http.MethodPost, "/unknown-provider", http.NoBody)

	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}
