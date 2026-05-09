package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/auth"
)

func TestPatchPlatformCreates(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPatch, "/api/platform", map[string]any{
		"domain": "example.com",
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
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// TestGetPlatformAsMember verifies members can read platform config.
func TestGetPlatformAsMember(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, _ := newTestServerAs(t, k8sClient, auth.RoleMember)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/platform", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for member reading platform, got %d: %s", w.Code, w.Body.String())
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

func TestPatchPlatformClearableFields(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	// Create with values set.
	w := doRequest(h, http.MethodPatch, "/api/platform", map[string]any{
		"domain":         "example.com",
		"domainTemplate": "{{.App}}.{{.Domain}}",
		"registry":       map[string]any{"url": "registry.example.com", "namespace": "myns"},
		"build":          map[string]any{"buildkitAddr": "tcp://buildkit:1234", "defaultPlatform": "linux/amd64"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Verify values are set.
	var pc mortisev1alpha1.PlatformConfig
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "platform"}, &pc); err != nil {
		t.Fatalf("get: %v", err)
	}
	if pc.Spec.DomainTemplate != "{{.App}}.{{.Domain}}" {
		t.Fatalf("domainTemplate not set: got %q", pc.Spec.DomainTemplate)
	}

	// Clear fields by sending empty strings.
	w = doRequest(h, http.MethodPatch, "/api/platform", map[string]any{
		"domainTemplate": "",
		"registry":       map[string]any{"url": "", "namespace": ""},
		"build":          map[string]any{"buildkitAddr": "", "defaultPlatform": ""},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("clear: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify fields were cleared.
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "platform"}, &pc); err != nil {
		t.Fatalf("get after clear: %v", err)
	}
	if pc.Spec.DomainTemplate != "" {
		t.Errorf("domainTemplate: expected empty, got %q", pc.Spec.DomainTemplate)
	}
	if pc.Spec.Registry.URL != "" {
		t.Errorf("registry.url: expected empty, got %q", pc.Spec.Registry.URL)
	}
	// Registry.Namespace reverts to kubebuilder default "mortise" when cleared.
	if pc.Spec.Registry.Namespace != "mortise" {
		t.Errorf("registry.namespace: expected kubebuilder default 'mortise', got %q", pc.Spec.Registry.Namespace)
	}
	if pc.Spec.Build.BuildkitAddr != "" {
		t.Errorf("build.buildkitAddr: expected empty, got %q", pc.Spec.Build.BuildkitAddr)
	}
	// Build.DefaultPlatform reverts to kubebuilder default "linux/amd64" when cleared.
	if pc.Spec.Build.DefaultPlatform != "linux/amd64" {
		t.Errorf("build.defaultPlatform: expected kubebuilder default 'linux/amd64', got %q", pc.Spec.Build.DefaultPlatform)
	}
	// Domain should NOT have been cleared (stays as string, not *string).
	if pc.Spec.Domain != "example.com" {
		t.Errorf("domain: expected example.com, got %q", pc.Spec.Domain)
	}
}

func TestPatchPlatformClearableObservabilityTokens(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPatch, "/api/platform", map[string]any{
		"observability": map[string]any{
			"logsAdapterToken":    "logs-token",
			"metricsAdapterToken": "metrics-token",
			"trafficAdapterToken": "traffic-token",
		},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var secret corev1.Secret
	secretKey := types.NamespacedName{Namespace: "mortise-system", Name: "observer-adapter-tokens"}
	if err := k8sClient.Get(context.Background(), secretKey, &secret); err != nil {
		t.Fatalf("get secret after create: %v", err)
	}
	if got := string(secret.Data["logs"]); got != "logs-token" {
		t.Fatalf("logs token: expected logs-token, got %q", got)
	}

	w = doRequest(h, http.MethodPatch, "/api/platform", map[string]any{
		"observability": map[string]any{
			"logsAdapterToken":    "",
			"metricsAdapterToken": "",
			"trafficAdapterToken": "",
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("clear: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if err := k8sClient.Get(context.Background(), secretKey, &secret); !errors.IsNotFound(err) {
		t.Fatalf("expected token secret deletion after clear, got err=%v", err)
	}
}

func TestPatchPlatformClearableObservabilityTokensDoesNotCreateEmptySecret(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPatch, "/api/platform", map[string]any{
		"domain": "example.com",
		"observability": map[string]any{
			"logsAdapterToken":    "",
			"metricsAdapterToken": "",
			"trafficAdapterToken": "",
		},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create with clear request: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var secret corev1.Secret
	secretKey := types.NamespacedName{Namespace: "mortise-system", Name: "observer-adapter-tokens"}
	if err := k8sClient.Get(context.Background(), secretKey, &secret); !errors.IsNotFound(err) {
		t.Fatalf("expected no empty token secret to be created, got err=%v", err)
	}

	var pc mortisev1alpha1.PlatformConfig
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "platform"}, &pc); err != nil {
		t.Fatalf("get platform after clear-only create: %v", err)
	}
	if pc.Spec.Observability.LogsAdapterTokenSecretRef != nil ||
		pc.Spec.Observability.MetricsAdapterTokenSecretRef != nil ||
		pc.Spec.Observability.TrafficAdapterTokenSecretRef != nil {
		t.Fatalf("expected no observability token refs, got %+v", pc.Spec.Observability)
	}
}
