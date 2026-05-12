package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/auth"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/envstore"
)

type projectCreateConflictClient struct {
	client.Client
	fired bool
}

func (c *projectCreateConflictClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	project, ok := obj.(*mortisev1alpha1.Project)
	if !ok || project.Name != "demo" || c.fired {
		return c.Client.Update(ctx, obj, opts...)
	}

	c.fired = true

	var live mortisev1alpha1.Project
	if err := c.Client.Get(ctx, types.NamespacedName{Name: project.Name}, &live); err != nil {
		return err
	}
	for i := range live.Spec.Environments {
		if live.Spec.Environments[i].Name == "production" {
			live.Spec.Environments[i].Restricted = true
			break
		}
	}
	if err := c.Client.Update(ctx, &live); err != nil {
		return err
	}

	return apierrors.NewConflict(
		schema.GroupResource{Group: mortisev1alpha1.GroupVersion.Group, Resource: "projects"},
		project.Name,
		fmt.Errorf("simulated conflict"),
	)
}

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

func TestListProjectEnvironmentsHealthRollupTreatsDegradedAsWarning(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "demo", "production")

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "app-degraded", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app.Status.Phase = mortisev1alpha1.AppPhaseDegraded
	if err := k8sClient.Status().Update(context.Background(), app); err != nil {
		t.Fatalf("update status: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/demo/environments", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var envs []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&envs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envs) != 1 {
		t.Fatalf("expected 1 env, got %d", len(envs))
	}
	if envs[0]["health"] != "warning" {
		t.Fatalf("expected degraded app to roll up as warning, got %v", envs[0]["health"])
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
	const projectName = "demo-create-env"
	seedProject(t, k8sClient, projectName)

	w := doRequest(h, http.MethodPost, "/api/projects/"+projectName+"/environments", map[string]any{
		"name":         "staging",
		"displayOrder": 5,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var proj mortisev1alpha1.Project
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: projectName}, &proj); err != nil {
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

func TestCreateProjectEnvironmentPreservesConcurrentProjectUpdate(t *testing.T) {
	baseClient := setupEnvtest(t)
	seedProject(t, baseClient, "demo")

	conflictClient := &projectCreateConflictClient{Client: baseClient}
	srv := newAdminServer(t, conflictClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/projects/demo/environments", map[string]any{
		"name":         "staging",
		"displayOrder": 5,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if !conflictClient.fired {
		t.Fatalf("expected simulated conflict to fire")
	}

	var proj mortisev1alpha1.Project
	if err := baseClient.Get(context.Background(), types.NamespacedName{Name: "demo"}, &proj); err != nil {
		t.Fatalf("get project: %v", err)
	}
	if len(proj.Spec.Environments) != 2 {
		t.Fatalf("expected 2 envs in spec, got %d", len(proj.Spec.Environments))
	}

	var production, staging *mortisev1alpha1.ProjectEnvironment
	for i := range proj.Spec.Environments {
		env := &proj.Spec.Environments[i]
		switch env.Name {
		case "production":
			production = env
		case "staging":
			staging = env
		}
	}
	if production == nil {
		t.Fatalf("production env missing: %+v", proj.Spec.Environments)
	}
	if staging == nil {
		t.Fatalf("staging env missing: %+v", proj.Spec.Environments)
	}
	if !production.Restricted {
		t.Errorf("expected concurrent production update to be preserved")
	}
	if staging.DisplayOrder != 5 {
		t.Errorf("staging displayOrder = %d, want 5", staging.DisplayOrder)
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

func TestUpdateProjectEnvironmentRenamePreservesConcurrentProjectUpdate(t *testing.T) {
	baseClient := setupEnvtest(t)
	ns := seedProject(t, baseClient, "demo", "production", "staging")

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source:       mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{{Name: "staging", Domain: "web-staging.example.com"}},
		},
	}
	if err := baseClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	conflictClient := &projectCreateConflictClient{Client: baseClient}
	srv := newAdminServer(t, conflictClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPatch, "/api/projects/demo/environments/staging", map[string]any{"name": "stage"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !conflictClient.fired {
		t.Fatalf("expected simulated conflict to fire")
	}

	var proj mortisev1alpha1.Project
	if err := baseClient.Get(context.Background(), types.NamespacedName{Name: "demo"}, &proj); err != nil {
		t.Fatalf("get project: %v", err)
	}
	var production, stage *mortisev1alpha1.ProjectEnvironment
	for i := range proj.Spec.Environments {
		env := &proj.Spec.Environments[i]
		switch env.Name {
		case "production":
			production = env
		case "stage":
			stage = env
		case "staging":
			t.Fatalf("old env name still present: %+v", proj.Spec.Environments)
		}
	}
	if production == nil || stage == nil {
		t.Fatalf("expected production and stage envs, got %+v", proj.Spec.Environments)
	}
	if !production.Restricted {
		t.Fatalf("expected concurrent production update to be preserved")
	}

	var got mortisev1alpha1.App
	if err := baseClient.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "web"}, &got); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if len(got.Spec.Environments) != 1 || got.Spec.Environments[0].Name != "stage" {
		t.Fatalf("app override not renamed: %+v", got.Spec.Environments)
	}
}

func TestUpdateProjectEnvironmentRenamePreservesSecretEnvVars(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	const projectName = "demo-rename-envvars"
	ns := seedProject(t, k8sClient, projectName, "production", "staging")

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{{
				Name: "staging",
				Env:  []mortisev1alpha1.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}},
			}},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	store := &envstore.Store{Client: k8sClient}
	if err := store.Set(context.Background(), constants.EnvNamespace(projectName, "staging"), "web", []envstore.Env{
		{Name: "EXTRA_FLAG", Value: "one", Source: "user"},
		{Name: "BINDING_ONLY", Value: "skip-me", Source: "binding"},
	}, nil); err != nil {
		t.Fatalf("seed env secret: %v", err)
	}

	w := doRequest(h, http.MethodPatch, "/api/projects/"+projectName+"/environments/staging", map[string]any{"name": "stage"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var got mortisev1alpha1.App
	_ = k8sClient.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "web"}, &got)
	if len(got.Spec.Environments) != 1 || got.Spec.Environments[0].Name != "stage" {
		t.Fatalf("app override not renamed: %+v", got.Spec.Environments)
	}

	envMap := map[string]string{}
	for _, ev := range got.Spec.Environments[0].Env {
		envMap[ev.Name] = ev.Value
	}
	if envMap["LOG_LEVEL"] != "debug" {
		t.Fatalf("expected LOG_LEVEL to be preserved, got %+v", got.Spec.Environments[0].Env)
	}
	if len(envMap) != 1 {
		t.Fatalf("expected only CRD env vars on the renamed override, got %+v", got.Spec.Environments[0].Env)
	}

	renamedVars, err := store.Get(context.Background(), constants.EnvNamespace(projectName, "stage"), "web")
	if err != nil {
		t.Fatalf("get renamed env secret: %v", err)
	}
	secretMap := map[string]string{}
	for _, env := range renamedVars {
		secretMap[env.Name] = env.Value
	}
	if secretMap["EXTRA_FLAG"] != "one" {
		t.Fatalf("expected EXTRA_FLAG to move with the env secret, got %+v", renamedVars)
	}
	if _, found := secretMap["BINDING_ONLY"]; found {
		t.Fatalf("expected binding-sourced vars to stay out of cloned env secrets, got %+v", renamedVars)
	}
}

func TestUpdateProjectEnvironmentRenamePreservesCustomSecrets(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	const projectName = "demo-rename-custom-secret"
	seedProject(t, k8sClient, projectName, "production", "staging")
	doRequest(h, http.MethodPost, "/api/projects/"+projectName+"/apps", map[string]any{
		"name": "webapp",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	stagingNs := constants.EnvNamespace(projectName, "staging")
	if err := k8sClient.Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "db-pass",
			Namespace: stagingNs,
			Labels: map[string]string{
				constants.AppNameLabel:         "webapp",
				constants.ProjectLabel:         projectName,
				"app.kubernetes.io/managed-by": "mortise",
			},
		},
		StringData: map[string]string{"PASSWORD": "secret"},
	}); err != nil {
		t.Fatalf("seed custom secret: %v", err)
	}

	w := doRequest(h, http.MethodPatch, "/api/projects/"+projectName+"/environments/staging", map[string]any{"name": "canary"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var copied corev1.Secret
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Namespace: constants.EnvNamespace(projectName, "canary"),
		Name:      "db-pass",
	}, &copied); err != nil {
		t.Fatalf("get copied custom secret: %v", err)
	}
	if string(copied.Data["PASSWORD"]) != "secret" {
		t.Fatalf("expected copied secret data, got %+v", copied.Data)
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

// TestDeleteProjectEnvironmentRejectsReferencedOverride verifies the REST API
// enforces the same protection as the optional admission webhook.
func TestDeleteProjectEnvironmentRejectsReferencedOverride(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "demo", "production", "staging")

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

	w := doRequest(h, http.MethodDelete, "/api/projects/demo/environments/staging", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}

	var proj mortisev1alpha1.Project
	_ = k8sClient.Get(context.Background(), types.NamespacedName{Name: "demo"}, &proj)
	if len(proj.Spec.Environments) != 2 {
		t.Fatalf("expected project envs unchanged, got %+v", proj.Spec.Environments)
	}

	var got mortisev1alpha1.App
	_ = k8sClient.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "web"}, &got)
	if len(got.Spec.Environments) != 1 || got.Spec.Environments[0].Name != "staging" {
		t.Fatalf("expected app override unchanged, got %+v", got.Spec.Environments)
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
	const projectName = "demo-clone-env"
	ns := seedProject(t, k8sClient, projectName)

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

	w := doRequest(h, http.MethodPost, "/api/projects/"+projectName+"/environments/production/clone", map[string]any{
		"name":         "staging",
		"displayOrder": 5,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Verify project has the new env.
	var proj mortisev1alpha1.Project
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: projectName}, &proj); err != nil {
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

func TestCloneProjectEnvironmentCopiesCustomSecrets(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	const projectName = "demo-clone-custom-secret"
	seedProject(t, k8sClient, projectName)
	doRequest(h, http.MethodPost, "/api/projects/"+projectName+"/apps", map[string]any{
		"name": "webapp",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	prodNs := constants.EnvNamespace(projectName, "production")
	if err := k8sClient.Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "api-key",
			Namespace: prodNs,
			Labels: map[string]string{
				constants.AppNameLabel:         "webapp",
				constants.ProjectLabel:         projectName,
				"app.kubernetes.io/managed-by": "mortise",
			},
		},
		StringData: map[string]string{"TOKEN": "abc123"},
	}); err != nil {
		t.Fatalf("seed custom secret: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/projects/"+projectName+"/environments/production/clone", map[string]any{
		"name": "staging",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var copied corev1.Secret
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Namespace: constants.EnvNamespace(projectName, "staging"),
		Name:      "api-key",
	}, &copied); err != nil {
		t.Fatalf("get copied custom secret: %v", err)
	}
	if string(copied.Data["TOKEN"]) != "abc123" {
		t.Fatalf("expected copied secret data, got %+v", copied.Data)
	}
}

func TestCloneProjectEnvironmentCopiesSecretEnvVarsWithoutLeakingIntoAppSpec(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	const projectName = "demo-clone-secret-env"
	ns := seedProject(t, k8sClient, projectName)

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
			Environments: []mortisev1alpha1.Environment{{
				Name: "production",
				Env:  []mortisev1alpha1.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}},
			}},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	store := &envstore.Store{Client: k8sClient}
	if err := store.Set(context.Background(), constants.EnvNamespace(projectName, "production"), "web", []envstore.Env{
		{Name: "EXTRA_FLAG", Value: "one", Source: "user"},
		{Name: "BINDING_ONLY", Value: "skip-me", Source: "binding"},
	}, nil); err != nil {
		t.Fatalf("seed env secret: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/projects/"+projectName+"/environments/production/clone", map[string]any{
		"name": "staging",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var got mortisev1alpha1.App
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "web"}, &got); err != nil {
		t.Fatalf("get app: %v", err)
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
	envMap := map[string]string{}
	for _, ev := range cloned.Env {
		envMap[ev.Name] = ev.Value
	}
	if envMap["LOG_LEVEL"] != "debug" {
		t.Fatalf("expected LOG_LEVEL on cloned override, got %+v", cloned.Env)
	}
	if envMap["EXTRA_FLAG"] != "one" {
		t.Fatalf("expected EXTRA_FLAG preserved on cloned override, got %+v", cloned.Env)
	}
	if len(envMap) != 2 {
		t.Fatalf("expected merged CRD + secret env vars on cloned override, got %+v", cloned.Env)
	}

	clonedVars, err := store.Get(context.Background(), constants.EnvNamespace(projectName, "staging"), "web")
	if err != nil {
		t.Fatalf("get cloned env secret: %v", err)
	}
	secretMap := map[string]string{}
	for _, env := range clonedVars {
		secretMap[env.Name] = env.Value
	}
	if secretMap["EXTRA_FLAG"] != "one" {
		t.Fatalf("expected EXTRA_FLAG in cloned env secret, got %+v", clonedVars)
	}
	if _, found := secretMap["BINDING_ONLY"]; found {
		t.Fatalf("expected binding-sourced vars to stay out of cloned env secret, got %+v", clonedVars)
	}
}

// TestCloneProjectEnvironmentNoSourceOverrides clones when apps have no
// explicit overrides for the source env — should create an empty env entry.
func TestCloneProjectEnvironmentNoSourceOverrides(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	const projectName = "demo-clone-empty"
	ns := seedProject(t, k8sClient, projectName)

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/projects/"+projectName+"/environments/production/clone", map[string]any{
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

// TestCloneProjectEnvironmentDuplicateTarget rejects cloning onto an existing
// target environment.
func TestCloneProjectEnvironmentDuplicateTarget(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "demo", "production", "staging")

	w := doRequest(h, http.MethodPost, "/api/projects/demo/environments/production/clone", map[string]any{
		"name": "staging",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
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
