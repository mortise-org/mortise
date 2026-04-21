package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/MC-Meesh/mortise/internal/api"
)

// TestCreateStackMissingSource verifies that omitting both compose and template
// returns 400.
func TestCreateStackMissingSource(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodPost, "/api/projects/default/stacks", map[string]any{})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when neither compose nor template provided, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCreateStackBothSourcesRejects verifies that providing both compose and
// template returns 400 (mutually exclusive).
func TestCreateStackBothSourcesRejects(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodPost, "/api/projects/default/stacks", map[string]any{
		"compose":  "services:\n  web:\n    image: nginx\n",
		"template": "supabase",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when both compose and template provided, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCreateStackInvalidComposeYAML verifies that malformed compose YAML
// returns 400.
func TestCreateStackInvalidComposeYAML(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodPost, "/api/projects/default/stacks", map[string]any{
		"compose": "not: valid: compose: {yaml",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid compose YAML, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCreateStackInvalidJSON verifies that malformed JSON returns 400.
func TestCreateStackInvalidJSON(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequestRawBody(h, http.MethodPost, "/api/projects/default/stacks", "{bad json", testToken)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCreateStackMissingProject verifies that creating a stack in a
// nonexistent project returns 404.
func TestCreateStackMissingProject(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/projects/ghost/stacks", map[string]any{
		"compose": "services:\n  web:\n    image: nginx\n",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing project, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCreateStackFilterExcludesSome verifies that when a filter matches a
// subset of services, only the matching ones are created (201 with survivors).
func TestCreateStackFilterExcludesSome(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	compose := `services:
  web:
    image: nginx:1.25
  db:
    image: postgres:15
  cache:
    image: redis:7
`
	body := map[string]any{
		"compose":  compose,
		"services": []string{"web", "db"},
		"name":     "mystack",
	}
	w := doRequest(h, http.MethodPost, "/api/projects/default/stacks", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 when filter keeps a subset, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Apps []string `json:"apps"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Apps) != 2 {
		t.Fatalf("expected 2 apps, got %d: %v", len(resp.Apps), resp.Apps)
	}
	got := map[string]bool{}
	for _, a := range resp.Apps {
		got[a] = true
	}
	if !got["mystack-web"] || !got["mystack-db"] {
		t.Errorf("expected web and db apps, got %v", resp.Apps)
	}
	if got["mystack-cache"] {
		t.Errorf("expected cache to be excluded, but it was created")
	}
}

// rngFailReader always fails on Read. Used to simulate a broken entropy source.
type rngFailReader struct{}

func (rngFailReader) Read(p []byte) (int, error) {
	return 0, errors.New("simulated rng failure")
}

// TestCreateStackRNGFailureReturns500 verifies that when the template engine
// cannot generate random secrets, the handler returns 500 (not 400, and
// crucially not 201 with a broken App containing empty passwords).
func TestCreateStackRNGFailureReturns500(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	restore := api.SwapRandReader(rngFailReader{})
	t.Cleanup(restore)

	body := map[string]any{"template": "supabase"}
	w := doRequest(h, http.MethodPost, "/api/projects/default/stacks", body)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on rng failure, got %d: %s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("simulated rng failure")) {
		t.Errorf("expected sanitised error, got raw: %s", w.Body.String())
	}
}

// TestCreateStackFilterExcludesAll verifies that when all requested services
// are filtered out (none exist in the compose), the handler returns 400 with
// a message listing the missing names.
func TestCreateStackFilterExcludesAll(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	compose := `services:
  web:
    image: nginx:1.25
  db:
    image: postgres:15
`
	body := map[string]any{
		"compose":  compose,
		"services": []string{"nope", "also-nope"},
	}
	w := doRequest(h, http.MethodPost, "/api/projects/default/stacks", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when filter excludes all services, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] == "" {
		t.Errorf("expected error message in body")
	}
	for _, want := range []string{"nope", "also-nope"} {
		if !strings.Contains(resp["error"], want) {
			t.Errorf("expected error %q to mention %q", resp["error"], want)
		}
	}
}
