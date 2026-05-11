package api_test

import (
	"net/http"
	"testing"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

func TestTrafficHistoryRejectsUndeclaredEnvironment(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")
	seedImageApp(t, k8sClient, ns, "traffic-app")

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/traffic-app/traffic?env=staging&start=1700000000&end=1700003600", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for undeclared env, got %d: %s", w.Code, w.Body.String())
	}
}

func TestTrafficHistoryRejectsDisabledAppEnvironment(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default", "staging", "production")
	disabled := false
	seedImageApp(t, k8sClient, ns, "traffic-app", mortisev1alpha1.Environment{Name: "staging", Enabled: &disabled})

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/traffic-app/traffic?env=staging&start=1700000000&end=1700003600", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for disabled env, got %d: %s", w.Code, w.Body.String())
	}
}

func TestTrafficCurrentRejectsUndeclaredEnvironment(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")
	seedImageApp(t, k8sClient, ns, "traffic-app")

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/traffic-app/traffic/current?env=staging", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for undeclared env, got %d: %s", w.Code, w.Body.String())
	}
}

func TestTrafficCurrentRejectsDisabledAppEnvironment(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default", "staging", "production")
	disabled := false
	seedImageApp(t, k8sClient, ns, "traffic-app", mortisev1alpha1.Environment{Name: "staging", Enabled: &disabled})

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/traffic-app/traffic/current?env=staging", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for disabled env, got %d: %s", w.Code, w.Body.String())
	}
}
