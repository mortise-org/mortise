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
	setupResp.Body.Close()

	type creds struct {
		email    string
		password string
	}
	loginOrder := []creds{{email: email, password: password}}
	if email != "admin@local" || password != "admin123" {
		// Reused clusters are often bootstrapped with these default credentials.
		loginOrder = append(loginOrder, creds{email: "admin@local", password: "admin123"})
	}

	tryLogin := func(c creds) (token string, status int, body string, err error) {
		loginBody, _ := json.Marshal(map[string]string{
			"email":    c.email,
			"password": c.password,
		})
		loginReq, _ := http.NewRequest(http.MethodPost, base+"/api/auth/login", bytes.NewReader(loginBody))
		loginReq.Header.Set("Content-Type", "application/json")
		loginResp, err := client.Do(loginReq)
		if err != nil {
			return "", 0, "", err
		}
		defer loginResp.Body.Close()

		status = loginResp.StatusCode
		if status != http.StatusOK {
			b, _ := io.ReadAll(loginResp.Body)
			return "", status, string(b), nil
		}

		var out struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(loginResp.Body).Decode(&out); err != nil {
			return "", status, "", err
		}
		return out.Token, status, "", nil
	}

	for i, c := range loginOrder {
		token, status, body, err := tryLogin(c)
		if err != nil {
			t.Fatalf("mortise: POST /api/auth/login: %v", err)
		}
		if status == http.StatusOK {
			if token == "" {
				t.Fatal("mortise: empty token in login response")
			}
			return token
		}
		if status == http.StatusUnauthorized && i < len(loginOrder)-1 {
			continue
		}
		t.Fatalf("mortise: POST /api/auth/login status %d: %s", status, body)
	}

	t.Fatal("mortise: unable to login with available admin credentials")
	return ""
}
