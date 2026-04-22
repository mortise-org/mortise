package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// TestListBindingsMergesPerEnv builds two apps — "web" with a binding to "db"
// only in production, and "disabled-app" opted out of production — and verifies
// the handler returns exactly the production edge.
func TestListBindingsMergesPerEnv(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "demo", "production", "staging")

	falsePtr := false
	apps := []*mortisev1alpha1.App{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
			Spec: mortisev1alpha1.AppSpec{
				Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
				Environments: []mortisev1alpha1.Environment{
					{Name: "production", Bindings: []mortisev1alpha1.Binding{{Ref: "db"}}},
					{Name: "staging"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: ns},
			Spec: mortisev1alpha1.AppSpec{
				Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "disabled-app", Namespace: ns},
			Spec: mortisev1alpha1.AppSpec{
				Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
				Environments: []mortisev1alpha1.Environment{
					{Name: "production", Enabled: &falsePtr, Bindings: []mortisev1alpha1.Binding{{Ref: "db"}}},
				},
			},
		},
	}
	for _, app := range apps {
		if err := k8sClient.Create(context.Background(), app); err != nil {
			t.Fatalf("create %s: %v", app.Name, err)
		}
	}

	w := doRequest(h, http.MethodGet, "/api/projects/demo/bindings?environment=production", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var edges []map[string]any
	_ = json.NewDecoder(w.Body).Decode(&edges)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge (disabled-app skipped), got %d: %+v", len(edges), edges)
	}
	if edges[0]["from"] != "web" || edges[0]["to"] != "db" || edges[0]["environment"] != "production" {
		t.Errorf("unexpected edge: %+v", edges[0])
	}

	// Staging: web's override has empty bindings → no edges.
	w = doRequest(h, http.MethodGet, "/api/projects/demo/bindings?environment=staging", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("staging: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	_ = json.NewDecoder(w.Body).Decode(&edges)
	if len(edges) != 0 {
		t.Fatalf("staging: expected 0 edges, got %d: %+v", len(edges), edges)
	}
}

// TestListBindingsRejectsUnknownEnv returns 400 for an env not on the project.
func TestListBindingsRejectsUnknownEnv(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "demo")

	w := doRequest(h, http.MethodGet, "/api/projects/demo/bindings?environment=ghost", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
