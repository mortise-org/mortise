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

func TestListDomains(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	// Create an app with environments.
	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{
				{
					Name:          "production",
					Domain:        "webapp.example.com",
					CustomDomains: []string{"alt.example.com"},
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/webapp/domains?environment=production", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["primary"] != "webapp.example.com" {
		t.Errorf("primary: expected webapp.example.com, got %v", resp["primary"])
	}
	custom := resp["custom"].([]any)
	if len(custom) != 1 || custom[0] != "alt.example.com" {
		t.Errorf("custom: expected [alt.example.com], got %v", custom)
	}
}

func TestAddDomain(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production", Domain: "webapp.example.com"},
			},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/webapp/domains?environment=production", map[string]string{
		"domain": "custom.example.com",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify CRD was updated.
	var updated mortisev1alpha1.App
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "webapp", Namespace: ns}, &updated); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if len(updated.Spec.Environments[0].CustomDomains) != 1 {
		t.Fatalf("expected 1 custom domain, got %d", len(updated.Spec.Environments[0].CustomDomains))
	}
	if updated.Spec.Environments[0].CustomDomains[0] != "custom.example.com" {
		t.Errorf("expected custom.example.com, got %s", updated.Spec.Environments[0].CustomDomains[0])
	}
}

func TestAddDomainDuplicate(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{
				{
					Name:          "production",
					Domain:        "webapp.example.com",
					CustomDomains: []string{"existing.example.com"},
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/webapp/domains?environment=production", map[string]string{
		"domain": "existing.example.com",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAddDomainInvalidHostname(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production", Domain: "webapp.example.com"},
			},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/webapp/domains?environment=production", map[string]string{
		"domain": "not a hostname!",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRemoveDomainNotFound(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production", CustomDomains: []string{"only.example.com"}},
			},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodDelete, "/api/projects/default/apps/webapp/domains/ghost.example.com?environment=production", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRemoveDomainNoEnvOverride(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodDelete, "/api/projects/default/apps/webapp/domains/ghost.example.com?environment=production", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when app has no env override, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDomainsMissingEnvParam(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/webapp/domains", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing env param, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDomainsNonexistentApp(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/ghost/domains?environment=production", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for nonexistent app, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDomainsUndeclaredEnv(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/webapp/domains?environment=ghost", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for undeclared env, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRemoveDomain(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{
				{
					Name:          "production",
					Domain:        "webapp.example.com",
					CustomDomains: []string{"remove-me.example.com", "keep-me.example.com"},
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodDelete, "/api/projects/default/apps/webapp/domains/remove-me.example.com?environment=production", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var updated mortisev1alpha1.App
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "webapp", Namespace: ns}, &updated); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if len(updated.Spec.Environments[0].CustomDomains) != 1 {
		t.Fatalf("expected 1 custom domain, got %d", len(updated.Spec.Environments[0].CustomDomains))
	}
	if updated.Spec.Environments[0].CustomDomains[0] != "keep-me.example.com" {
		t.Errorf("expected keep-me.example.com, got %s", updated.Spec.Environments[0].CustomDomains[0])
	}
}

func TestValidateDomain_NoConflict(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/domains/validate", map[string]string{
		"domain": "available.example.com",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["valid"] != true {
		t.Errorf("expected valid=true, got %v", resp["valid"])
	}
	if resp["conflict"] != nil {
		t.Errorf("expected no conflict, got %v", resp["conflict"])
	}
}

func TestValidateDomain_ConflictPrimary(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production", Domain: "taken.example.com"},
			},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/domains/validate", map[string]string{
		"domain": "taken.example.com",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["valid"] != false {
		t.Errorf("expected valid=false, got %v", resp["valid"])
	}
	conflict := resp["conflict"].(map[string]any)
	if conflict["project"] != "default" {
		t.Errorf("expected project=default, got %v", conflict["project"])
	}
	if conflict["app"] != "webapp" {
		t.Errorf("expected app=webapp, got %v", conflict["app"])
	}
	if conflict["environment"] != "production" {
		t.Errorf("expected environment=production, got %v", conflict["environment"])
	}
}

func TestValidateDomain_ConflictCustom(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{
				{
					Name:          "production",
					Domain:        "api.example.com",
					CustomDomains: []string{"custom.example.com"},
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/domains/validate", map[string]string{
		"domain": "custom.example.com",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["valid"] != false {
		t.Errorf("expected valid=false, got %v", resp["valid"])
	}
}

func TestValidateDomain_InvalidHostname(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/domains/validate", map[string]string{
		"domain": "not a valid hostname!",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestValidateDomain_SelfExclusion(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production", Domain: "myapp.example.com"},
			},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	// Without exclusion, the domain conflicts with itself.
	w := doRequest(h, http.MethodPost, "/api/domains/validate", map[string]any{
		"domain": "myapp.example.com",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["valid"] != false {
		t.Fatalf("expected conflict without exclusion, got valid=true")
	}

	// With self-exclusion, the domain is valid.
	w = doRequest(h, http.MethodPost, "/api/domains/validate", map[string]any{
		"domain":          "myapp.example.com",
		"exclude_app":     "webapp",
		"exclude_project": "default",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp = nil
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["valid"] != true {
		t.Errorf("expected valid=true with self-exclusion, got %v", resp["valid"])
	}
}

func TestValidateDomain_ConflictStatusDomain(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "autogen", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app.Status.Environments = []mortisev1alpha1.EnvironmentStatus{
		{Name: "production", Domain: "autogen-default.apps.example.com"},
	}
	if err := k8sClient.Status().Update(ctx, app); err != nil {
		t.Fatalf("update status: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/domains/validate", map[string]string{
		"domain": "autogen-default.apps.example.com",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["valid"] != false {
		t.Errorf("expected valid=false for auto-generated domain collision, got %v", resp["valid"])
	}
}

func TestAddDomain_RejectsStatusDomainCollision(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	other := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "other-app", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(ctx, other); err != nil {
		t.Fatalf("create other app: %v", err)
	}
	other.Status.Environments = []mortisev1alpha1.EnvironmentStatus{
		{Name: "production", Domain: "other-default.apps.example.com"},
	}
	if err := k8sClient.Status().Update(ctx, other); err != nil {
		t.Fatalf("update other status: %v", err)
	}

	target := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "target-app", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	if err := k8sClient.Create(ctx, target); err != nil {
		t.Fatalf("create target app: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/target-app/domains?environment=production", map[string]string{
		"domain": "other-default.apps.example.com",
	})
	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListDomains_AutoDomainFromStatus(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production", Domain: "custom.example.com"},
			},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app.Status.Environments = []mortisev1alpha1.EnvironmentStatus{
		{Name: "production", Domain: "custom.example.com", AutoDomain: "webapp-default.apps.example.com"},
	}
	if err := k8sClient.Status().Update(ctx, app); err != nil {
		t.Fatalf("update status: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/webapp/domains?environment=production", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["auto"] != "webapp-default.apps.example.com" {
		t.Errorf("auto: expected webapp-default.apps.example.com, got %v", resp["auto"])
	}
	if resp["primary"] != "custom.example.com" {
		t.Errorf("primary: expected custom.example.com, got %v", resp["primary"])
	}
}

func TestAddDomain_RejectsAutoDomainCollision(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	other := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "other-app", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(ctx, other); err != nil {
		t.Fatalf("create other app: %v", err)
	}
	other.Status.Environments = []mortisev1alpha1.EnvironmentStatus{
		{Name: "production", Domain: "primary.example.com", AutoDomain: "other-auto.apps.example.com"},
	}
	if err := k8sClient.Status().Update(ctx, other); err != nil {
		t.Fatalf("update other status: %v", err)
	}

	target := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "target-app", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	if err := k8sClient.Create(ctx, target); err != nil {
		t.Fatalf("create target app: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/target-app/domains?environment=production", map[string]string{
		"domain": "other-auto.apps.example.com",
	})
	if w.Code != http.StatusConflict {
		t.Errorf("expected 409 for AutoDomain collision, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAddDomain_RejectsOwnAutoDomain(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app.Status.Environments = []mortisev1alpha1.EnvironmentStatus{
		{Name: "production", AutoDomain: "webapp-default.apps.example.com"},
	}
	if err := k8sClient.Status().Update(ctx, app); err != nil {
		t.Fatalf("update status: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/webapp/domains?environment=production", map[string]string{
		"domain": "webapp-default.apps.example.com",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for app auto-domain duplicate, got %d: %s", w.Code, w.Body.String())
	}

	var updated mortisev1alpha1.App
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "webapp", Namespace: ns}, &updated); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if len(updated.Spec.Environments[0].CustomDomains) != 0 {
		t.Fatalf("expected no custom domains to be added, got %v", updated.Spec.Environments[0].CustomDomains)
	}
}

func TestValidateDomain_ConflictAutoDomain(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "autogen", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app.Status.Environments = []mortisev1alpha1.EnvironmentStatus{
		{Name: "production", Domain: "primary.example.com", AutoDomain: "autogen-auto.apps.example.com"},
	}
	if err := k8sClient.Status().Update(ctx, app); err != nil {
		t.Fatalf("update status: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/domains/validate", map[string]string{
		"domain": "autogen-auto.apps.example.com",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["valid"] != false {
		t.Errorf("expected valid=false for AutoDomain collision, got %v", resp["valid"])
	}
	conflict := resp["conflict"].(map[string]any)
	if conflict["app"] != "autogen" {
		t.Errorf("expected app=autogen, got %v", conflict["app"])
	}
}
