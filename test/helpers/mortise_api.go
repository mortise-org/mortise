package helpers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// LoginAsAdmin returns a Mortise JWT for an admin principal identified by
// (email, password), bootstrapping first-user setup if necessary. Idempotent
// across test reruns: if setup has already been completed (409 from
// /api/auth/setup) the function falls through to /api/auth/login.
//
// baseURL must be the root of the Mortise API (e.g. "http://127.0.0.1:43210").
// No trailing slash.
func LoginAsAdmin(t *testing.T, baseURL, email, password string) string {
	t.Helper()

	base := strings.TrimRight(baseURL, "/")
	client := &http.Client{}

	// Best-effort first-user setup. Conflict (409) means another actor (prior
	// test, prior run) already owns the platform — that's fine, we still try
	// to log in with the credentials the caller provided.
	setupBody, _ := json.Marshal(map[string]string{
		"email":    email,
		"password": password,
	})
	setupReq, _ := http.NewRequest(http.MethodPost, base+"/api/auth/setup",
		bytes.NewReader(setupBody))
	setupReq.Header.Set("Content-Type", "application/json")
	setupResp, err := client.Do(setupReq)
	if err != nil {
		t.Fatalf("mortise: POST /api/auth/setup: %v", err)
	}
	func() {
		defer setupResp.Body.Close()
		if setupResp.StatusCode == http.StatusCreated {
			// Setup succeeded; the response body already includes a token we
			// could reuse, but going through /api/auth/login keeps the flow
			// identical on fresh vs. reused clusters.
			return
		}
		if setupResp.StatusCode == http.StatusConflict {
			return // already bootstrapped, fine
		}
		b, _ := io.ReadAll(setupResp.Body)
		t.Fatalf("mortise: POST /api/auth/setup status %d: %s",
			setupResp.StatusCode, string(b))
	}()

	loginBody, _ := json.Marshal(map[string]string{
		"email":    email,
		"password": password,
	})
	loginReq, _ := http.NewRequest(http.MethodPost, base+"/api/auth/login",
		bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginResp, err := client.Do(loginReq)
	if err != nil {
		t.Fatalf("mortise: POST /api/auth/login: %v", err)
	}
	defer loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(loginResp.Body)
		t.Fatalf("mortise: POST /api/auth/login status %d: %s",
			loginResp.StatusCode, string(b))
	}

	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(loginResp.Body).Decode(&out); err != nil {
		t.Fatalf("mortise: decode login response: %v", err)
	}
	if out.Token == "" {
		t.Fatal("mortise: empty token in login response")
	}
	return out.Token
}
