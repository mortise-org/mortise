package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/activity"
	"github.com/mortise-org/mortise/internal/auth"
)

type activityEventResponse struct {
	TS       string `json:"ts"`
	Actor    string `json:"actor"`
	Action   string `json:"action"`
	Kind     string `json:"kind"`
	Resource string `json:"resource"`
	Project  string `json:"project"`
	Msg      string `json:"msg"`
}

func TestListActivityEmpty(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodGet, "/api/projects/default/activity", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var events []activityEventResponse
	if err := json.NewDecoder(w.Body).Decode(&events); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected empty activity list, got %d", len(events))
	}
}

func TestListActivityNewestFirstAndLimit(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	seedProject(t, k8sClient, "default")
	store := activity.NewConfigMapStore(k8sClient)
	if err := store.Append(context.Background(), activity.Event{
		Timestamp:    time.Now().Add(-2 * time.Minute),
		Actor:        "a@example.com",
		Action:       "create",
		ResourceKind: "app",
		ResourceName: "web",
		Project:      "default",
		Message:      "Created app web",
	}); err != nil {
		t.Fatalf("append first event: %v", err)
	}
	if err := store.Append(context.Background(), activity.Event{
		Timestamp:    time.Now().Add(-1 * time.Minute),
		Actor:        "a@example.com",
		Action:       "update",
		ResourceKind: "app",
		ResourceName: "web",
		Project:      "default",
		Message:      "Updated app web",
	}); err != nil {
		t.Fatalf("append second event: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/activity?limit=1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var events []activityEventResponse
	if err := json.NewDecoder(w.Body).Decode(&events); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Action != "update" {
		t.Fatalf("expected newest event action=update, got %q", events[0].Action)
	}
}

func TestDeployActivityActorIncludesTokenName(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	seedProject(t, k8sClient, "default")

	createApp := doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "web",
		"spec": map[string]any{
			"source":       map[string]any{"type": "image", "image": "nginx:1.27"},
			"environments": []map[string]any{{"name": "production"}},
		},
	})
	if createApp.Code != http.StatusCreated {
		t.Fatalf("create app: expected 201, got %d: %s", createApp.Code, createApp.Body.String())
	}

	createToken := doRequest(h, http.MethodPost, "/api/projects/default/apps/web/tokens", map[string]any{
		"name":        "ci",
		"environment": "production",
	})
	if createToken.Code != http.StatusCreated {
		t.Fatalf("create token: expected 201, got %d: %s", createToken.Code, createToken.Body.String())
	}
	var tokenResp map[string]any
	if err := json.NewDecoder(createToken.Body).Decode(&tokenResp); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	rawToken, _ := tokenResp["token"].(string)
	if rawToken == "" {
		t.Fatalf("create token response missing token")
	}

	deploy := doRequestWithToken(h, http.MethodPost, "/api/projects/default/apps/web/deploy", map[string]any{
		"environment": "production",
		"image":       "nginx:1.28",
	}, rawToken)
	if deploy.Code != http.StatusOK {
		t.Fatalf("deploy: expected 200, got %d: %s", deploy.Code, deploy.Body.String())
	}

	list := doRequest(h, http.MethodGet, "/api/projects/default/activity", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list activity: expected 200, got %d: %s", list.Code, list.Body.String())
	}

	var events []activityEventResponse
	if err := json.NewDecoder(list.Body).Decode(&events); err != nil {
		t.Fatalf("decode activity response: %v", err)
	}

	found := false
	for _, e := range events {
		if e.Action == "deploy" && e.Resource == "web" && e.Actor == "token:ci" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected deploy activity actor token:ci, got events: %+v", events)
	}
}

func TestListActivityLimitValidation(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodGet, "/api/projects/default/activity?limit=abc", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListPlatformActivityMergedAcrossProjects(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	seedProject(t, k8sClient, "alpha")
	seedProject(t, k8sClient, "beta")
	store := activity.NewConfigMapStore(k8sClient)
	if err := store.Append(context.Background(), activity.Event{
		Timestamp:    time.Now().Add(-2 * time.Minute),
		Actor:        "a@example.com",
		Action:       "deploy",
		ResourceKind: "app",
		ResourceName: "api",
		Project:      "alpha",
		Message:      "Deployed api",
	}); err != nil {
		t.Fatalf("append alpha event: %v", err)
	}
	if err := store.Append(context.Background(), activity.Event{
		Timestamp:    time.Now().Add(-1 * time.Minute),
		Actor:        "b@example.com",
		Action:       "build",
		ResourceKind: "app",
		ResourceName: "web",
		Project:      "beta",
		Message:      "Built web",
	}); err != nil {
		t.Fatalf("append beta event: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/activity?limit=10", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var events []activityEventResponse
	if err := json.NewDecoder(w.Body).Decode(&events); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Project != "beta" || events[1].Project != "alpha" {
		t.Fatalf("expected newest-first merged projects [beta, alpha], got [%s, %s]", events[0].Project, events[1].Project)
	}
}

func TestListPlatformActivityHonorsProjectAccess(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, _ := newTestServerAs(t, k8sClient, auth.RoleMember)
	h := srv.Handler()

	seedProject(t, k8sClient, "allowed")
	seedProject(t, k8sClient, "blocked")
	seedProjectMember(t, k8sClient, "allowed", "member@example.com", mortisev1alpha1.ProjectRoleViewer)

	store := activity.NewConfigMapStore(k8sClient)
	if err := store.Append(context.Background(), activity.Event{
		Timestamp:    time.Now().Add(-2 * time.Minute),
		Actor:        "member@example.com",
		Action:       "deploy",
		ResourceKind: "app",
		ResourceName: "ok",
		Project:      "allowed",
		Message:      "Allowed event",
	}); err != nil {
		t.Fatalf("append allowed event: %v", err)
	}
	if err := store.Append(context.Background(), activity.Event{
		Timestamp:    time.Now().Add(-1 * time.Minute),
		Actor:        "other@example.com",
		Action:       "deploy",
		ResourceKind: "app",
		ResourceName: "nope",
		Project:      "blocked",
		Message:      "Blocked event",
	}); err != nil {
		t.Fatalf("append blocked event: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/activity?limit=10", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var events []activityEventResponse
	if err := json.NewDecoder(w.Body).Decode(&events); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 visible event, got %d", len(events))
	}
	if events[0].Project != "allowed" {
		t.Fatalf("expected only allowed project event, got project=%q", events[0].Project)
	}
}

func TestListPlatformActivityLimitValidation(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/activity?limit=abc", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
