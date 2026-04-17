package git

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// generateTestPEM creates a fresh RSA private key PEM for testing.
func generateTestPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	block := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	return pem.EncodeToMemory(block)
}

func TestNewGitHubAppAPI_ParsesPEM(t *testing.T) {
	pemBytes := generateTestPEM(t)
	api, err := NewGitHubAppAPI("", 12345, pemBytes, "wh-secret")
	if err != nil {
		t.Fatalf("NewGitHubAppAPI: %v", err)
	}
	if api.appID != 12345 {
		t.Errorf("appID: got %d, want 12345", api.appID)
	}
	if api.secret != "wh-secret" {
		t.Errorf("secret: got %q, want wh-secret", api.secret)
	}
}

func TestNewGitHubAppAPI_InvalidPEM(t *testing.T) {
	_, err := NewGitHubAppAPI("", 1, []byte("not a pem"), "secret")
	if err == nil {
		t.Error("expected error for invalid PEM")
	}
}

func TestGitHubAppAPI_GenerateJWT(t *testing.T) {
	pemBytes := generateTestPEM(t)
	api, err := NewGitHubAppAPI("", 42, pemBytes, "")
	if err != nil {
		t.Fatalf("NewGitHubAppAPI: %v", err)
	}
	jwtStr, err := api.generateJWT()
	if err != nil {
		t.Fatalf("generateJWT: %v", err)
	}
	if jwtStr == "" {
		t.Error("expected non-empty JWT")
	}
}

func TestGitHubAppAPI_VerifyWebhookSignature(t *testing.T) {
	pemBytes := generateTestPEM(t)
	api, err := NewGitHubAppAPI("", 1, pemBytes, "test-secret")
	if err != nil {
		t.Fatalf("NewGitHubAppAPI: %v", err)
	}

	body := []byte(`{"action":"opened","number":1}`)
	mac := hmac.New(sha256.New, []byte("test-secret"))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	hdr := http.Header{"X-Hub-Signature-256": []string{"sha256=" + sig}}
	if err := api.VerifyWebhookSignature(body, hdr); err != nil {
		t.Errorf("expected nil, got: %v", err)
	}

	badHdr := http.Header{"X-Hub-Signature-256": []string{"sha256=0000000000000000000000000000000000000000000000000000000000000000"}}
	if err := api.VerifyWebhookSignature(body, badHdr); err == nil {
		t.Error("expected error for invalid signature")
	}

	if err := api.VerifyWebhookSignature(body, http.Header{}); err == nil {
		t.Error("expected error for missing header")
	}
}

func TestGitHubAppAPI_InstallationTokenCaching(t *testing.T) {
	pemBytes := generateTestPEM(t)
	api, err := NewGitHubAppAPI("", 1, pemBytes, "")
	if err != nil {
		t.Fatalf("NewGitHubAppAPI: %v", err)
	}

	api.mu.Lock()
	api.cachedToken = "cached-token"
	api.cachedTokenExp = time.Now().Add(30 * time.Minute)
	api.installationID = 42
	api.mu.Unlock()

	tok, err := api.installationToken(context.Background())
	if err != nil {
		t.Fatalf("installationToken: %v", err)
	}
	if tok != "cached-token" {
		t.Errorf("expected cached-token, got %q", tok)
	}
}

func TestGitHubAppAPI_ListRepos(t *testing.T) {
	pemBytes := generateTestPEM(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/installation/repositories":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"total_count": 1,
				"repositories": []map[string]interface{}{
					{
						"id":             1,
						"full_name":      "octo/hello",
						"name":           "hello",
						"description":    "A test repo",
						"default_branch": "main",
						"clone_url":      "https://github.com/octo/hello.git",
						"updated_at":     "2025-03-01T12:00:00Z",
						"language":       "Go",
						"private":        false,
						"owner":          map[string]interface{}{"login": "octo"},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	api, err := NewGitHubAppAPI(srv.URL, 1, pemBytes, "")
	if err != nil {
		t.Fatalf("NewGitHubAppAPI: %v", err)
	}
	api.mu.Lock()
	api.cachedToken = "test-tok"
	api.cachedTokenExp = time.Now().Add(30 * time.Minute)
	api.installationID = 42
	api.mu.Unlock()

	repos, err := api.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(repos))
	}
	if repos[0].FullName != "octo/hello" {
		t.Errorf("FullName: got %q", repos[0].FullName)
	}
}

func TestGitHubAppAPI_SetInstallationID(t *testing.T) {
	pemBytes := generateTestPEM(t)
	api, err := NewGitHubAppAPI("", 1, pemBytes, "")
	if err != nil {
		t.Fatalf("NewGitHubAppAPI: %v", err)
	}

	api.mu.Lock()
	api.cachedToken = "old"
	api.cachedTokenExp = time.Now().Add(30 * time.Minute)
	api.installationID = 1
	api.mu.Unlock()

	api.SetInstallationID(2)

	api.mu.Lock()
	defer api.mu.Unlock()
	if api.installationID != 2 {
		t.Errorf("installationID: got %d, want 2", api.installationID)
	}
	if api.cachedToken != "" {
		t.Errorf("cached token should be invalidated, got %q", api.cachedToken)
	}
}
