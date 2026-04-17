package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/auth"
)

func TestPatchPlatformCreates(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPatch, "/api/platform", map[string]any{
		"domain": "example.com",
		"dns":    map[string]any{"provider": "cloudflare"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["domain"] != "example.com" {
		t.Errorf("domain: expected example.com, got %v", resp["domain"])
	}

	// Verify CRD was created.
	var pc mortisev1alpha1.PlatformConfig
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "platform"}, &pc); err != nil {
		t.Fatalf("get PlatformConfig: %v", err)
	}
	if pc.Spec.Domain != "example.com" {
		t.Errorf("CRD domain: expected example.com, got %s", pc.Spec.Domain)
	}
}

func TestPatchPlatformUpdates(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	// First PATCH creates.
	w := doRequest(h, http.MethodPatch, "/api/platform", map[string]any{
		"domain": "example.com",
		"dns":    map[string]any{"provider": "cloudflare"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Second PATCH updates.
	w = doRequest(h, http.MethodPatch, "/api/platform", map[string]any{
		"domain": "new.example.com",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["domain"] != "new.example.com" {
		t.Errorf("domain: expected new.example.com, got %v", resp["domain"])
	}
}

func TestPatchPlatformForbiddenForMember(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, _ := newTestServerAs(t, k8sClient, auth.RoleMember)
	h := srv.Handler()

	w := doRequest(h, http.MethodPatch, "/api/platform", map[string]any{
		"domain": "example.com",
		"dns":    map[string]any{"provider": "cloudflare"},
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetPlatformEmpty(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/platform", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["domain"] != "" {
		t.Errorf("domain: expected empty, got %v", resp["domain"])
	}
}
