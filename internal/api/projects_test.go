package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/auth"
)

// TestCreateProjectAsAdmin verifies an admin can create a project via the API.
func TestCreateProjectAsAdmin(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/projects", map[string]any{
		"name":        "my-saas",
		"description": "customer-facing stack",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["name"] != "my-saas" {
		t.Errorf("expected name my-saas, got %v", resp["name"])
	}
	if resp["namespace"] != "pj-my-saas" {
		t.Errorf("expected namespace pj-my-saas, got %v", resp["namespace"])
	}

	// CRD must exist in the cluster.
	var project mortisev1alpha1.Project
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "my-saas"}, &project); err != nil {
		t.Fatalf("project CRD not found after create: %v", err)
	}
}

// TestCreateProjectInvalidName verifies the API rejects names that cannot be
// used as a DNS-1123 label or that would exceed namespace-name limits. The
// caller should see a 400 with a descriptive error, not a CRD-layer 422 or
// a project that gets stuck in Failed phase once the controller tries to
// create an invalid namespace.
func TestCreateProjectInvalidName(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	cases := []struct{ name, input string }{
		{"empty", ""},
		{"uppercase", "Bad_Name"},
		{"underscore", "bad_name"},
		{"leading-hyphen", "-bad"},
		{"too-long", "a-very-very-very-very-very-very-very-very-very-long-name-that-exceeds-dns-limits"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doRequest(h, http.MethodPost, "/api/projects", map[string]any{"name": tc.input})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for %q, got %d: %s", tc.input, w.Code, w.Body.String())
			}
		})
	}
}

// TestCreateProjectAsMember verifies members can create projects.
func TestCreateProjectAsMember(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, _ := newTestServerAs(t, k8sClient, auth.RoleMember)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/projects", map[string]any{"name": "member-proj"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for member creating project, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCreateProjectAsViewerForbidden verifies platform viewers cannot create projects.
func TestCreateProjectAsViewerForbidden(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, _ := newTestServerAs(t, k8sClient, auth.RoleViewer)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/projects", map[string]any{"name": "blocked"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer creating project, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDeleteProjectAsMemberForbidden verifies members cannot delete projects.
func TestDeleteProjectAsMemberForbidden(t *testing.T) {
	k8sClient := setupEnvtest(t)
	seedProject(t, k8sClient, "default")
	srv, _ := newTestServerAs(t, k8sClient, auth.RoleMember)
	h := srv.Handler()

	w := doRequest(h, http.MethodDelete, "/api/projects/default", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for member deleting project, got %d: %s", w.Code, w.Body.String())
	}
}

// TestListProjectsAsMember verifies members only see projects they belong to.
func TestListProjectsAsMember(t *testing.T) {
	k8sClient := setupEnvtest(t)
	seedProject(t, k8sClient, "visible")
	seedProject(t, k8sClient, "hidden")
	srv, _ := newTestServerAs(t, k8sClient, auth.RoleMember)
	h := srv.Handler()

	seedProjectMember(t, k8sClient, "visible", "member@example.com", mortisev1alpha1.ProjectRoleDeveloper)

	w := doRequest(h, http.MethodGet, "/api/projects", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for member listing projects, got %d: %s", w.Code, w.Body.String())
	}
	var projects []map[string]any
	_ = json.NewDecoder(w.Body).Decode(&projects)
	if len(projects) != 1 {
		t.Fatalf("expected 1 project (only the one with membership), got %d", len(projects))
	}
}

// TestGetProjectAsMember verifies project members can read a single project.
func TestGetProjectAsMember(t *testing.T) {
	k8sClient := setupEnvtest(t)
	seedProject(t, k8sClient, "readable")
	srv, _ := newTestServerAs(t, k8sClient, auth.RoleMember)
	h := srv.Handler()

	seedProjectMember(t, k8sClient, "readable", "member@example.com", mortisev1alpha1.ProjectRoleViewer)

	w := doRequest(h, http.MethodGet, "/api/projects/readable", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for member getting project, got %d: %s", w.Code, w.Body.String())
	}
}

// TestGetProjectNonMemberForbidden verifies non-members cannot read a project.
func TestGetProjectNonMemberForbidden(t *testing.T) {
	k8sClient := setupEnvtest(t)
	seedProject(t, k8sClient, "private")
	srv, _ := newTestServerAs(t, k8sClient, auth.RoleMember)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/projects/private", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-member, got %d: %s", w.Code, w.Body.String())
	}
}

// TestListProjects verifies the list handler returns every Project in the cluster.
func TestListProjects(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	for _, name := range []string{"alpha", "beta"} {
		doRequest(h, http.MethodPost, "/api/projects", map[string]any{"name": name})
	}

	w := doRequest(h, http.MethodGet, "/api/projects", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var projects []map[string]any
	_ = json.NewDecoder(w.Body).Decode(&projects)
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
}

// TestGetProjectNotFound verifies GET on a missing project returns 404.
func TestGetProjectNotFound(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/projects/ghost", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDeleteProjectReturns202 verifies deleting a project returns 202 accepted
// and marks the underlying CRD for deletion.
func TestDeleteProjectReturns202(t *testing.T) {
	k8sClient := setupEnvtest(t)
	seedProject(t, k8sClient, "victim")
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodDelete, "/api/projects/victim", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 accepted, got %d: %s", w.Code, w.Body.String())
	}

	// In envtest without a running controller the Project won't actually be
	// garbage-collected (no finalizer runs) — but DeletionTimestamp should be
	// set. For a simpler assertion we just verify the CRD was issued a delete:
	// either it's gone or its DeletionTimestamp is set.
	var project mortisev1alpha1.Project
	err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "victim"}, &project)
	if errors.IsNotFound(err) {
		return
	}
	if err != nil {
		t.Fatalf("get project after delete: %v", err)
	}
	if project.DeletionTimestamp == nil || project.DeletionTimestamp.IsZero() {
		t.Error("expected DeletionTimestamp to be set after DELETE")
	}
}

// TestCreateAppLandsInProjectNamespace verifies an app created via the project
// route ends up in the project's backing namespace.
func TestCreateAppLandsInProjectNamespace(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	nsName := seedProject(t, k8sClient, "webstack")

	w := doRequest(h, http.MethodPost, "/api/projects/webstack/apps", map[string]any{
		"name": "frontend",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var app mortisev1alpha1.App
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "frontend", Namespace: nsName}, &app); err != nil {
		t.Fatalf("app should exist in %s: %v", nsName, err)
	}
}

// TestCreateAppInMissingProjectIs404 verifies apps can only be created inside
// projects that exist.
func TestCreateAppInMissingProjectIs404(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/projects/ghost/apps", map[string]any{
		"name": "anything",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for app creation in missing project, got %d: %s", w.Code, w.Body.String())
	}
}

// TestListAppsIsolatedByProject verifies apps in project A aren't listed under
// project B.
func TestListAppsIsolatedByProject(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "alpha")
	seedProject(t, k8sClient, "beta")

	doRequest(h, http.MethodPost, "/api/projects/alpha/apps", map[string]any{
		"name": "a-app",
		"spec": map[string]any{"source": map[string]any{"type": "image", "image": "nginx:1.25.0"}},
	})
	doRequest(h, http.MethodPost, "/api/projects/beta/apps", map[string]any{
		"name": "b-app",
		"spec": map[string]any{"source": map[string]any{"type": "image", "image": "nginx:1.25.0"}},
	})

	w := doRequest(h, http.MethodGet, "/api/projects/alpha/apps", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list alpha apps: expected 200, got %d", w.Code)
	}
	var apps []mortisev1alpha1.App
	_ = json.NewDecoder(w.Body).Decode(&apps)
	if len(apps) != 1 || apps[0].Name != "a-app" {
		t.Fatalf("expected only a-app in alpha, got %+v", apps)
	}

	w = doRequest(h, http.MethodGet, "/api/projects/beta/apps", nil)
	_ = json.NewDecoder(w.Body).Decode(&apps)
	if len(apps) != 1 || apps[0].Name != "b-app" {
		t.Fatalf("expected only b-app in beta, got %+v", apps)
	}
}
