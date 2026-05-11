package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/api"
	"github.com/mortise-org/mortise/internal/auth"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
)

func seedExecApp(t *testing.T, k8sClient client.Client, projectName, appName string) {
	t.Helper()
	if err := k8sClient.Create(context.Background(), &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      appName,
			Namespace: constants.ControlNamespace(projectName),
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:  mortisev1alpha1.SourceTypeImage,
				Image: "nginx:1.25.0",
			},
		},
	}); err != nil {
		t.Fatalf("create app: %v", err)
	}
}

// TestServerCarriesInjectedRESTConfig verifies the constructor plumbs the
// rest.Config through onto the Server (instead of the handler calling
// rest.InClusterConfig() per request at runtime).
func TestServerCarriesInjectedRESTConfig(t *testing.T) {
	k8sClient := setupEnvtest(t)
	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	cfg := &rest.Config{Host: "https://example.test"}

	srv := api.NewServer(k8sClient, fake.NewClientset(), nil, cfg, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	if srv.RESTConfig() == nil {
		t.Fatal("expected Server.RESTConfig() to return the injected config, got nil")
	}
	if srv.RESTConfig().Host != "https://example.test" {
		t.Errorf("expected host https://example.test, got %q", srv.RESTConfig().Host)
	}
}

// TestExecEmptyCommand verifies the handler rejects an empty command list.
func TestExecEmptyCommand(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")
	seedImageApp(t, k8sClient, ns, "anything")

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/anything/exec", map[string]any{
		"command": []string{},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty command, got %d: %s", w.Code, w.Body.String())
	}
}

// TestExecInvalidJSON verifies the handler rejects malformed JSON.
func TestExecInvalidJSON(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")
	seedImageApp(t, k8sClient, ns, "anything")

	w := doRequestRawBody(h, http.MethodPost, "/api/projects/default/apps/anything/exec", "{bad json", testToken)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d: %s", w.Code, w.Body.String())
	}
}

// TestExecMissingProject verifies exec returns 404 for a nonexistent project.
func TestExecMissingProject(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/projects/ghost/apps/anything/exec", map[string]any{
		"command": []string{"ls"},
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing project, got %d: %s", w.Code, w.Body.String())
	}
}

// TestExecRejectsWhenNoRESTConfig verifies the handler fails fast with 500
// (not a panic, not a silent in-cluster fallback) when the server was built
// without a rest.Config — e.g. in test harnesses that don't exercise exec.
func TestExecRejectsWhenNoRESTConfig(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")
	seedImageApp(t, k8sClient, ns, "anything")

	body := map[string]any{"command": []string{"ls"}}
	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/anything/exec", body)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when server has no rest.Config, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] == "" {
		t.Errorf("expected error message in body")
	}
}

func TestExecMissingAppReturns404BeforePodLookup(t *testing.T) {
	cs := fake.NewClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "phantom-pod",
				Namespace: constants.EnvNamespace("default", "production"),
				Labels: map[string]string{
					constants.AppNameLabel:         "ghost",
					constants.EnvironmentLabel:     "production",
					"app.kubernetes.io/managed-by": "mortise",
				},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)
	var podLists int32
	cs.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		atomic.AddInt32(&podLists, 1)
		return false, nil, nil
	})

	k8sClient := setupEnvtest(t)
	srv, token := newExecServerWithClientset(t, k8sClient, cs, &rest.Config{Host: "https://example.test"})
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequestWithToken(h, http.MethodPost, "/api/projects/default/apps/ghost/exec?env=production", map[string]any{
		"command": []string{"sh", "-c", "echo hi"},
	}, token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing app, got %d: %s", w.Code, w.Body.String())
	}
	if got := atomic.LoadInt32(&podLists); got != 0 {
		t.Fatalf("expected no pod lookup for missing app, got %d pod list calls", got)
	}
}

func TestExecReturns500WhenPodLookupFails(t *testing.T) {
	cs := fake.NewClientset()
	cs.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, context.DeadlineExceeded
	})

	k8sClient := setupEnvtest(t)
	srv, token := newExecServerWithClientset(t, k8sClient, cs, &rest.Config{Host: "https://example.test"})
	h := srv.Handler()
	seedProject(t, k8sClient, "default")
	seedExecApp(t, k8sClient, "default", "anything")

	w := doRequestWithToken(h, http.MethodPost, "/api/projects/default/apps/anything/exec?env=production", map[string]any{
		"command": []string{"sh", "-c", "echo hi"},
	}, token)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for pod lookup failure, got %d: %s", w.Code, w.Body.String())
	}
}
func TestExecRejectsUndeclaredEnvironment(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")
	seedImageApp(t, k8sClient, ns, "anything")

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/anything/exec?env=staging", map[string]any{
		"command": []string{"ls"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for undeclared env, got %d: %s", w.Code, w.Body.String())
	}
}

func TestExecRejectsDisabledAppEnvironment(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default", "staging", "production")
	disabled := false
	seedImageApp(t, k8sClient, ns, "anything", mortisev1alpha1.Environment{Name: "staging", Enabled: &disabled})

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/anything/exec?env=staging", map[string]any{
		"command": []string{"ls"},
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for disabled env, got %d: %s", w.Code, w.Body.String())
	}
}

func newExecServerWithClientset(t *testing.T, k8sClient client.Client, cs *fake.Clientset, cfg *rest.Config) (*api.Server, string) {
	t.Helper()
	ctx := context.Background()

	_ = k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"},
	})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	if err := authProvider.CreateUser(ctx, "test@example.com", "testpass", auth.RoleAdmin); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	principal, err := authProvider.Authenticate(ctx, auth.Credentials{Email: "test@example.com", Password: "testpass"})
	if err != nil {
		t.Fatalf("authenticate test user: %v", err)
	}
	token, err := jwtHelper.GenerateToken(ctx, principal)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	srv := api.NewServer(k8sClient, cs, nil, cfg, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	return srv, token
}
