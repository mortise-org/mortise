package helpers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestLoginAsAdminReturnsSetupTokenWithoutLoginRetry(t *testing.T) {
	var loginCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/setup":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": "setup-token",
				"user":  map[string]any{"email": "admin@example.com"},
			})
		case "/api/auth/login":
			loginCalls.Add(1)
			http.Error(w, `{"error":"unexpected login"}`, http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	token := LoginAsAdmin(t, srv.URL, "admin@example.com", "password123")
	if token != "setup-token" {
		t.Fatalf("expected setup token, got %q", token)
	}
	if got := loginCalls.Load(); got != 0 {
		t.Fatalf("expected no login retry after successful setup, got %d call(s)", got)
	}
}

func TestLoginAsAdminFallsBackToLoginAfterSetupConflict(t *testing.T) {
	var loginCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/setup":
			http.Error(w, `{"error":"setup already complete"}`, http.StatusConflict)
		case "/api/auth/login":
			loginCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "login-token"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	token := LoginAsAdmin(t, srv.URL, "admin@example.com", "password123")
	if token != "login-token" {
		t.Fatalf("expected login token, got %q", token)
	}
	if got := loginCalls.Load(); got != 1 {
		t.Fatalf("expected one login attempt after setup conflict, got %d", got)
	}
}

func TestLoginAsAdminTreatsSetupAsBestEffort(t *testing.T) {
	var loginCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/setup":
			http.Error(w, `{"error":"transient setup failure"}`, http.StatusInternalServerError)
		case "/api/auth/login":
			loginCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "login-token"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	token := LoginAsAdmin(t, srv.URL, "admin@example.com", "password123")
	if token != "login-token" {
		t.Fatalf("expected login token, got %q", token)
	}
	if got := loginCalls.Load(); got != 1 {
		t.Fatalf("expected one login attempt after setup failure, got %d", got)
	}
}
