package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/auth"
)

// TestListProjectEnvironmentsEmptyProject verifies that listing envs on a
// project with no apps returns the project's env list with unknown health.
func TestListProjectEnvironmentsEmptyProject(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "demo", "production", "staging")

	w := doRequest(h, http.MethodGet, "/api/projects/demo/environments", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var envs []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&envs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envs) != 2 {
		t.Fatalf("expected 2 envs, got %d", len(envs))
	}
	// Ordered by DisplayOrder (seedProject uses slice index).
	if envs[0]["name"] != "production" || envs[1]["name"] != "staging" {
		t.Errorf("unexpected order: %+v", envs)
	}
	for _, env := range envs {
		if env["health"] != "unknown" {
			t.Errorf("env %q: expected unknown health with no apps, got %v", env["name"], env["health"])
		}
	}
}

// TestListProjectEnvironmentsHealthRollup verifies the dot-health aggregation
// across participating apps: all Ready → healthy; any Building → warning; any
// Failed → danger.
func TestListProjectEnvironmentsHealthRollup(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "demo", "production", "staging")

	// app1: Ready
	// app2: Failed
	// app3: Building
	for _, tc := range []struct {
		name  string
		phase mortisev1alpha1.AppPhase
	}{
		{"app1", mortisev1alpha1.AppPhaseReady},
		{"app2", mortisev1alpha1.AppPhaseFailed},
		{"app3", mortisev1alpha1.AppPhaseBuilding},
	} {
		app := &mortisev1alpha1.App{
			ObjectMeta: metav1.ObjectMeta{Name: tc.name, Namespace: ns},
			Spec: mortisev1alpha1.AppSpec{
				Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
			},
		}
		if err := k8sClient.Create(context.Background(), app); err != nil {
			t.Fatalf("create %s: %v", tc.name, err)
		}
		app.Status.Phase = tc.phase
		app.Status.Environments = []mortisev1alpha1.EnvironmentStatus{{Name: "production", ReadyReplicas: 1}}
		if err := k8sClient.Status().Update(context.Background(), app); err != nil {
			t.Fatalf("update status %s: %v", tc.name, err)
		}
	}

	w := doRequest(h, http.MethodGet, "/api/projects/demo/environments", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var envs []map[string]any
	_ = json.NewDecoder(w.Body).Decode(&envs)
	envByName := map[string]map[string]any{}
	for _, e := range envs {
		envByName[e["name"].(string)] = e
	}
	// production: Failed wins → danger
	if got := envByName["production"]["health"]; got != "danger" {
		t.Errorf("production: expected danger, got %v", got)
	}
}

// TestListProjectEnvironmentsPerEnvPhase verifies that env health uses per-env
// phase rather than the aggregate app phase. An app deploying in production
// should not make staging show as warning when staging is Ready.
func TestListProjectEnvironmentsPerEnvPhase(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "perenv", "production", "staging")

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app.Status.Phase = mortisev1alpha1.AppPhaseDeploying
	app.Status.Environments = []mortisev1alpha1.EnvironmentStatus{
		{Name: "production", Phase: mortisev1alpha1.AppPhaseDeploying, ReadyReplicas: 0},
		{Name: "staging", Phase: mortisev1alpha1.AppPhaseReady, ReadyReplicas: 1},
	}
	if err := k8sClient.Status().Update(context.Background(), app); err != nil {
		t.Fatalf("update status: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/perenv/environments", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var envs []map[string]any
	_ = json.NewDecoder(w.Body).Decode(&envs)
	envByName := map[string]map[string]any{}
	for _, e := range envs {
		envByName[e["name"].(string)] = e
	}
	if got := envByName["production"]["health"]; got != "warning" {
		t.Errorf("production: expected warning, got %v", got)
	}
	if got := envByName["staging"]["health"]; got != "healthy" {
		t.Errorf("staging: expected healthy, got %v", got)
	}
}

// TestCreateProjectEnvironment verifies admins can append a new env.
func TestCreateProjectEnvironment(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "demo")

	w := doRequest(h, http.MethodPost, "/api/projects/demo/environments", map[string]any{
		"name":         "staging",
		"displayOrder": 5,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var proj mortisev1alpha1.Project
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "demo"}, &proj); err != nil {
		t.Fatalf("get project: %v", err)
	}
	if len(proj.Spec.Environments) != 2 {
		t.Fatalf("expected 2 envs in spec, got %d", len(proj.Spec.Environments))
	}
	added := proj.Spec.Environments[1]
	if added.Name != "staging" || added.DisplayOrder != 5 {
		t.Errorf("unexpected env on spec: %+v", added)
	}
}

// TestCreateProjectEnvironmentDuplicate returns 409 on an existing env name.
func TestCreateProjectEnvironmentDuplicate(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "demo", "production", "staging")

	w := doRequest(h, http.MethodPost, "/api/projects/demo/environments", map[string]any{"name": "staging"})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCreateProjectEnvironmentInvalidName rejects non-DNS-label names.
func TestCreateProjectEnvironmentInvalidName(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "demo")

	for _, bad := range []string{"", "UPPER", "has space", "ends-", "-starts"} {
		w := doRequest(h, http.MethodPost, "/api/projects/demo/environments", map[string]any{"name": bad})
		if w.Code != http.StatusBadRequest {
			t.Errorf("name %q: expected 400, got %d: %s", bad, w.Code, w.Body.String())
		}
	}
}

// TestCreateProjectEnvironmentAsMemberForbidden verifies members cannot create envs.
func TestCreateProjectEnvironmentAsMemberForbidden(t *testing.T) {
	k8sClient := setupEnvtest(t)
	seedProject(t, k8sClient, "demo")
	srv, _ := newTestServerAs(t, k8sClient, auth.RoleMember)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/projects/demo/environments", map[string]any{"name": "staging"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// TestUpdateProjectEnvironmentAsMemberForbidden verifies members cannot update envs.
func TestUpdateProjectEnvironmentAsMemberForbidden(t *testing.T) {
	k8sClient := setupEnvtest(t)
	seedProject(t, k8sClient, "demo", "production", "staging")
	srv, _ := newTestServerAs(t, k8sClient, auth.RoleMember)
	h := srv.Handler()

	w := doRequest(h, http.MethodPatch, "/api/projects/demo/environments/staging", map[string]any{"displayOrder": 10})
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDeleteProjectEnvironmentAsMemberForbidden verifies members cannot delete envs.
func TestDeleteProjectEnvironmentAsMemberForbidden(t *testing.T) {
	k8sClient := setupEnvtest(t)
	seedProject(t, k8sClient, "demo", "production", "staging")
	srv, _ := newTestServerAs(t, k8sClient, auth.RoleMember)
	h := srv.Handler()

	w := doRequest(h, http.MethodDelete, "/api/projects/demo/environments/staging", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// TestUpdateProjectEnvironmentRename renames an env and cascades to any App
// overrides in the project namespace.
func TestUpdateProjectEnvironmentRename(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "demo", "production", "staging")

	// App with an override on the env being renamed.
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source:       mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{{Name: "staging", Domain: "web-staging.example.com"}},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodPatch, "/api/projects/demo/environments/staging", map[string]any{"name": "stage"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Project spec updated.
	var proj mortisev1alpha1.Project
	_ = k8sClient.Get(context.Background(), types.NamespacedName{Name: "demo"}, &proj)
	found := false
	for _, env := range proj.Spec.Environments {
		if env.Name == "stage" {
			found = true
		}
		if env.Name == "staging" {
			t.Errorf("old name still present on project")
		}
	}
	if !found {
		t.Errorf("renamed env not on project spec: %+v", proj.Spec.Environments)
	}

	// App override renamed.
	var got mortisev1alpha1.App
	_ = k8sClient.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "web"}, &got)
	if len(got.Spec.Environments) != 1 || got.Spec.Environments[0].Name != "stage" {
		t.Errorf("app override not renamed: %+v", got.Spec.Environments)
	}
}

// TestUpdateProjectEnvironmentReorder updates only the displayOrder.
func TestUpdateProjectEnvironmentReorder(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "demo", "production", "staging")

	w := doRequest(h, http.MethodPatch, "/api/projects/demo/environments/staging", map[string]any{"displayOrder": 10})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var proj mortisev1alpha1.Project
	_ = k8sClient.Get(context.Background(), types.NamespacedName{Name: "demo"}, &proj)
	for _, env := range proj.Spec.Environments {
		if env.Name == "staging" && env.DisplayOrder != 10 {
			t.Errorf("expected displayOrder 10, got %d", env.DisplayOrder)
		}
	}
}

// TestUpdateProjectEnvironmentRenameConflict rejects a rename onto an existing name.
func TestUpdateProjectEnvironmentRenameConflict(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "demo", "production", "staging")

	w := doRequest(h, http.MethodPatch, "/api/projects/demo/environments/staging", map[string]any{"name": "production"})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

// TestUpdateProjectEnvironmentNotFound returns 404 for a missing env name.
func TestUpdateProjectEnvironmentNotFound(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "demo")

	w := doRequest(h, http.MethodPatch, "/api/projects/demo/environments/ghost", map[string]any{"displayOrder": 1})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDeleteProjectEnvironment removes a non-last env.
func TestDeleteProjectEnvironment(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "demo", "production", "staging")

	w := doRequest(h, http.MethodDelete, "/api/projects/demo/environments/staging", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var proj mortisev1alpha1.Project
	_ = k8sClient.Get(context.Background(), types.NamespacedName{Name: "demo"}, &proj)
	if len(proj.Spec.Environments) != 1 || proj.Spec.Environments[0].Name != "production" {
		t.Errorf("unexpected envs after delete: %+v", proj.Spec.Environments)
	}
}

// TestDeleteProjectEnvironmentRejectsLast refuses to delete the only env.
func TestDeleteProjectEnvironmentRejectsLast(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "demo")

	w := doRequest(h, http.MethodDelete, "/api/projects/demo/environments/production", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDeleteProjectEnvironmentNotFound returns 404 for an unknown env.
func TestDeleteProjectEnvironmentNotFound(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "demo", "production", "staging")

	w := doRequest(h, http.MethodDelete, "/api/projects/demo/environments/ghost", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestAppEnvRejectsUnknownProjectEnv verifies app-env endpoints refuse env
// names not declared on the parent project.
func TestAppEnvRejectsUnknownProjectEnv(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "demo")

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/demo/apps/web/env?environment=ghost", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCloneProjectEnvironment clones production→staging and verifies the
// new env appears on the project and all app overrides are copied.
func TestCloneProjectEnvironment(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "demo")

	replicas := int32(3)
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{{
				Name:      "production",
				Replicas:  &replicas,
				Resources: mortisev1alpha1.ResourceRequirements{CPU: "500m", Memory: "512Mi"},
				Env: []mortisev1alpha1.EnvVar{
					{Name: "DATABASE_URL", Value: "postgres://prod:5432/app"},
					{Name: "LOG_LEVEL", Value: "warn"},
				},
				Bindings: []mortisev1alpha1.Binding{{Ref: "redis"}},
			}},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/projects/demo/environments/production/clone", map[string]any{
		"name":         "staging",
		"displayOrder": 5,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Verify project has the new env.
	var proj mortisev1alpha1.Project
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "demo"}, &proj); err != nil {
		t.Fatalf("get project: %v", err)
	}
	if len(proj.Spec.Environments) != 2 {
		t.Fatalf("expected 2 envs, got %d", len(proj.Spec.Environments))
	}

	// Verify app has cloned env overrides.
	var got mortisev1alpha1.App
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "web"}, &got); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if len(got.Spec.Environments) != 2 {
		t.Fatalf("expected 2 env overrides on app, got %d", len(got.Spec.Environments))
	}

	var cloned *mortisev1alpha1.Environment
	for i := range got.Spec.Environments {
		if got.Spec.Environments[i].Name == "staging" {
			cloned = &got.Spec.Environments[i]
			break
		}
	}
	if cloned == nil {
		t.Fatal("staging env override not found on app")
	}
	if cloned.Replicas == nil || *cloned.Replicas != 3 {
		t.Errorf("expected replicas 3, got %v", cloned.Replicas)
	}
	if cloned.Resources.CPU != "500m" || cloned.Resources.Memory != "512Mi" {
		t.Errorf("resources not cloned: %+v", cloned.Resources)
	}
	if len(cloned.Env) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(cloned.Env))
	}
	if cloned.Env[0].Name != "DATABASE_URL" || cloned.Env[0].Value != "postgres://prod:5432/app" {
		t.Errorf("unexpected env[0]: %+v", cloned.Env[0])
	}
	if len(cloned.Bindings) != 1 || cloned.Bindings[0].Ref != "redis" {
		t.Errorf("bindings not cloned: %+v", cloned.Bindings)
	}
}

// TestCloneProjectEnvironmentNoSourceOverrides clones when apps have no
// explicit overrides for the source env — should create an empty env entry.
func TestCloneProjectEnvironmentNoSourceOverrides(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "demo")

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/projects/demo/environments/production/clone", map[string]any{
		"name": "staging",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var got mortisev1alpha1.App
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "api"}, &got); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if len(got.Spec.Environments) != 1 {
		t.Fatalf("expected 1 env override, got %d", len(got.Spec.Environments))
	}
	if got.Spec.Environments[0].Name != "staging" {
		t.Errorf("expected staging, got %q", got.Spec.Environments[0].Name)
	}
}

// TestCloneProjectEnvironmentSourceNotFound returns 404 when the source doesn't exist.
func TestCloneProjectEnvironmentSourceNotFound(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "demo")

	w := doRequest(h, http.MethodPost, "/api/projects/demo/environments/ghost/clone", map[string]any{
		"name": "staging",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCloneProjectEnvironmentDuplicateTarget returns 200 (retry-safe) when
// the target env already exists on the project — the handler skips the
// project update and continues with per-app cloning.
func TestCloneProjectEnvironmentDuplicateTarget(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "demo", "production", "staging")

	w := doRequest(h, http.MethodPost, "/api/projects/demo/environments/production/clone", map[string]any{
		"name": "staging",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (retry-safe), got %d: %s", w.Code, w.Body.String())
	}
}

// TestCloneProjectEnvironmentInvalidTargetName returns 400 for bad target names.
func TestCloneProjectEnvironmentInvalidTargetName(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "demo")

	w := doRequest(h, http.MethodPost, "/api/projects/demo/environments/production/clone", map[string]any{
		"name": "INVALID",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
