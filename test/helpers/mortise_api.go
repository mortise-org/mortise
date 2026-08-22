package helpers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// doWithRetry issues an HTTP request built by newReq, retrying on transport
// errors (connection reset, EOF, etc.) a bounded number of times. PortForward
// proves the tunnel serves a request before handing back its port, but
// kubectl can still drop and re-establish the tunnel in the window right
// after that, so a single reset on the very next request shouldn't be fatal.
// newReq rebuilds the request each attempt since a request with a body can't
// be replayed.
func doWithRetry(client *http.Client, newReq func() (*http.Request, error)) (*http.Response, error) {
	const attempts = 5
	var lastErr error
	for i := 0; i < attempts; i++ {
		req, err := newReq()
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	return nil, lastErr
}

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
	setupResp, err := doWithRetry(client, func() (*http.Request, error) {
		req, err := http.NewRequest(http.MethodPost, base+"/api/auth/setup", bytes.NewReader(setupBody))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
	if err != nil {
		t.Fatalf("mortise: POST /api/auth/setup: %v", err)
	}

	var setupAuth struct {
		Token string `json:"token"`
	}
	func() {
		defer setupResp.Body.Close()
		if setupResp.StatusCode == http.StatusCreated {
			if err := json.NewDecoder(setupResp.Body).Decode(&setupAuth); err != nil {
				t.Fatalf("mortise: decode /api/auth/setup response: %v", err)
			}
			if setupAuth.Token == "" {
				t.Fatal("mortise: empty token in setup response")
			}
			return
		}
		_, _ = io.Copy(io.Discard, setupResp.Body)
	}()
	if setupAuth.Token != "" {
		return setupAuth.Token
	}

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
		loginResp, err := doWithRetry(client, func() (*http.Request, error) {
			req, err := http.NewRequest(http.MethodPost, base+"/api/auth/login", bytes.NewReader(loginBody))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/json")
			return req, nil
		})
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

	// When another parallel test wins /api/auth/setup first, the loser can
	// briefly observe 409 from setup before the new admin secret is readable by
	// /api/auth/login. Retry boundedly instead of failing on that transient 401.
	deadline := time.Now().Add(3 * time.Second)
	for {
		lastStatus := 0
		lastBody := ""
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
			lastStatus = status
			lastBody = body
			if status == http.StatusUnauthorized && i < len(loginOrder)-1 {
				continue
			}
			if status != http.StatusUnauthorized {
				t.Fatalf("mortise: POST /api/auth/login status %d: %s", status, body)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("mortise: POST /api/auth/login status %d: %s", lastStatus, lastBody)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
