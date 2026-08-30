package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVersion_PrintsOperatorIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/platform"; got != want {
			t.Errorf("unexpected path: got %q, want %q", got, want)
		}
		var resp PlatformResponse
		resp.Operator.Version = "1.1.0"
		resp.Operator.Commit = "5e3011e0aa30f00"
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	var buf bytes.Buffer
	if err := printOperatorVersion(&buf, newTestClient(srv)); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "Operator: 1.1.0 (5e3011e0aa30f00)\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestVersion_OldOperatorReportsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(PlatformResponse{Domain: "example.com"})
	}))
	defer srv.Close()

	var buf bytes.Buffer
	if err := printOperatorVersion(&buf, newTestClient(srv)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Operator: unknown") {
		t.Errorf("expected unknown marker, got %q", buf.String())
	}
}

func TestVersion_ClientOnlyDoesNotNeedLogin(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"version", "--client"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), "Client:   0.0.0-dev (unknown)") {
		t.Errorf("unexpected output %q", buf.String())
	}
}
