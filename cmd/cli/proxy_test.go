package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyConnect_EncodesEnvironmentQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects/myproject/apps/web/connect"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if got, want := r.URL.Query().Get("environment"), "staging"; got != want {
			t.Errorf("unexpected environment query: got %q, want %q", got, want)
		}
		_ = json.NewEncoder(w).Encode(ConnectResponse{Port: 1234, URL: "http://localhost:1234"})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	if _, err := c.Connect(c.ResolveProject(""), "web", "staging"); err != nil {
		t.Fatal(err)
	}
}

func TestProxyDisconnect_EncodesEnvironmentQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects/myproject/apps/web/disconnect"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		if got, want := r.URL.Query().Get("environment"), "staging"; got != want {
			t.Errorf("unexpected environment query: got %q, want %q", got, want)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	if err := c.Disconnect(c.ResolveProject(""), "web", "staging"); err != nil {
		t.Fatal(err)
	}
}
