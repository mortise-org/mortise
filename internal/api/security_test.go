package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- Fix 2: Body size limit ---

// TestBodySizeLimitRejects413 verifies that a >1MB POST to /api/apps returns
// 413 or 400 (MaxBytesReader surfaces as a decode error).
func TestBodySizeLimitRejects413(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	seedProject(t, k8sClient, "limit-test")
	h := srv.Handler()

	// Build a body that exceeds 1MB.
	bigBody := `{"name":"test","spec":{"source":{"type":"image","image":"` + strings.Repeat("x", 1<<20+1) + `"}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/limit-test/apps", strings.NewReader(bigBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	// http.MaxBytesReader causes either 413 (if the framework handles it) or
	// 400 (because json.Decode fails with a max-bytes error). Both are acceptable
	// rejections. 201 would be a failure.
	if w.Code == http.StatusCreated || w.Code == http.StatusOK {
		t.Fatalf("expected rejection for >1MB body, got %d", w.Code)
	}
	if w.Code != http.StatusRequestEntityTooLarge && w.Code != http.StatusBadRequest {
		t.Fatalf("expected 413 or 400, got %d: %s", w.Code, w.Body.String())
	}
}

// --- Fix 3: App name validation ---

// TestCreateAppInvalidName verifies the API rejects invalid app names with 400.
func TestCreateAppInvalidName(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	seedProject(t, k8sClient, "val-test")
	h := srv.Handler()

	cases := []struct {
		label string
		name  string
	}{
		{"empty", ""},
		{"uppercase", "BadApp"},
		{"dots", "my.app"},
		{"leading-hyphen", "-bad"},
		{"trailing-hyphen", "bad-"},
		{"too-long", strings.Repeat("a", 54)}, // exceeds maxAppNameLen=53
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			w := doRequest(h, http.MethodPost, "/api/projects/val-test/apps", map[string]any{
				"name": tc.name,
				"spec": map[string]any{
					"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
				},
			})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for name %q, got %d: %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

// TestCreateAppValidName confirms a valid name passes.
func TestCreateAppValidName(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	seedProject(t, k8sClient, "val-ok")
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/projects/val-ok/apps", map[string]any{
		"name": "good-app",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCreateAppInvalidEnvironmentName verifies env name validation on app creation.
func TestCreateAppInvalidEnvironmentName(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	seedProject(t, k8sClient, "env-val")
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/projects/env-val/apps", map[string]any{
		"name": "valid-app",
		"spec": map[string]any{
			"source":       map[string]any{"type": "image", "image": "nginx:1.25.0"},
			"environments": []map[string]any{{"name": "BAD_ENV"}},
		},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid env name, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp["error"], "environments[0].name") {
		t.Errorf("error should mention environments[0].name, got: %s", resp["error"])
	}
}
