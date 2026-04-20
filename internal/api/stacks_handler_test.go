package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

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
