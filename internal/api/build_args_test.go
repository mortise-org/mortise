package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

func TestGetBuildArgs_Empty(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "git", Repo: "https://github.com/org/repo"},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/webapp/build-args", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp []map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if len(resp) != 0 {
		t.Errorf("expected empty build args, got %v", resp)
	}
}

func TestGetBuildArgs_WithArgs(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type: "git",
				Repo: "https://github.com/org/repo",
				Build: &mortisev1alpha1.Build{
					Args: map[string]string{
						"VITE_API_URL": "https://api.example.com",
						"NODE_ENV":     "production",
					},
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/webapp/build-args", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp []map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if len(resp) != 2 {
		t.Fatalf("expected 2 build args, got %d", len(resp))
	}

	found := make(map[string]string)
	for _, item := range resp {
		found[item["name"]] = item["value"]
	}
	if found["VITE_API_URL"] != "https://api.example.com" {
		t.Errorf("VITE_API_URL: expected https://api.example.com, got %s", found["VITE_API_URL"])
	}
	if found["NODE_ENV"] != "production" {
		t.Errorf("NODE_ENV: expected production, got %s", found["NODE_ENV"])
	}
}

func TestPutBuildArgs(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "git", Repo: "https://github.com/org/repo"},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	body := []map[string]string{
		{"name": "VITE_API_URL", "value": "https://api.example.com"},
		{"name": "NODE_ENV", "value": "production"},
	}
	w := doRequest(h, http.MethodPut, "/api/projects/default/apps/webapp/build-args", body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify CRD was updated.
	var updated mortisev1alpha1.App
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "webapp", Namespace: ns}, &updated); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if updated.Spec.Source.Build == nil {
		t.Fatal("expected Build to be non-nil")
	}
	if updated.Spec.Source.Build.Args["VITE_API_URL"] != "https://api.example.com" {
		t.Errorf("VITE_API_URL: expected https://api.example.com, got %s", updated.Spec.Source.Build.Args["VITE_API_URL"])
	}
	if updated.Spec.Source.Build.Args["NODE_ENV"] != "production" {
		t.Errorf("NODE_ENV: expected production, got %s", updated.Spec.Source.Build.Args["NODE_ENV"])
	}
}

func TestPutBuildArgs_ReplaceExisting(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type: "git",
				Repo: "https://github.com/org/repo",
				Build: &mortisev1alpha1.Build{
					Args: map[string]string{"OLD_KEY": "old_value"},
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	body := []map[string]string{
		{"name": "NEW_KEY", "value": "new_value"},
	}
	w := doRequest(h, http.MethodPut, "/api/projects/default/apps/webapp/build-args", body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var updated mortisev1alpha1.App
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "webapp", Namespace: ns}, &updated); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if _, exists := updated.Spec.Source.Build.Args["OLD_KEY"]; exists {
		t.Error("OLD_KEY should have been removed")
	}
	if updated.Spec.Source.Build.Args["NEW_KEY"] != "new_value" {
		t.Errorf("NEW_KEY: expected new_value, got %s", updated.Spec.Source.Build.Args["NEW_KEY"])
	}
}

func TestPutBuildArgs_SkipsEmptyKeys(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "git", Repo: "https://github.com/org/repo"},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	body := []map[string]string{
		{"name": "", "value": "should_be_skipped"},
		{"name": "VALID", "value": "kept"},
	}
	w := doRequest(h, http.MethodPut, "/api/projects/default/apps/webapp/build-args", body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var updated mortisev1alpha1.App
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "webapp", Namespace: ns}, &updated); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if len(updated.Spec.Source.Build.Args) != 1 {
		t.Fatalf("expected 1 arg, got %d", len(updated.Spec.Source.Build.Args))
	}
	if updated.Spec.Source.Build.Args["VALID"] != "kept" {
		t.Errorf("VALID: expected kept, got %s", updated.Spec.Source.Build.Args["VALID"])
	}
}

func TestGetBuildArgs_NotFound(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	_ = seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/nonexistent/build-args", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}
