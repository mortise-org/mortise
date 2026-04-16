package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/api"
)

func setupEnvtest(t *testing.T) client.Client {
	t.Helper()

	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")},
	}

	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { testEnv.Stop() })

	err = mortisev1alpha1.AddToScheme(scheme.Scheme)
	if err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	// Ensure default namespace exists.
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
	k8sClient.Create(context.Background(), ns)

	return k8sClient
}

func doRequest(handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestCreateAndGetApp(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := api.NewServer(k8sClient, fake.NewClientset())
	h := srv.Handler()

	createBody := map[string]any{
		"name":      "myapp",
		"namespace": "default",
		"spec": map[string]any{
			"source": map[string]any{
				"type":  "image",
				"image": "nginx:1.25.0",
			},
		},
	}

	w := doRequest(h, http.MethodPost, "/api/apps", createBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("create app: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// GET by name
	w = doRequest(h, http.MethodGet, "/api/apps/myapp?namespace=default", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get app: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var app mortisev1alpha1.App
	json.NewDecoder(w.Body).Decode(&app)
	if app.Spec.Source.Image != "nginx:1.25.0" {
		t.Errorf("expected image nginx:1.25.0, got %s", app.Spec.Source.Image)
	}
}

func TestListApps(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := api.NewServer(k8sClient, fake.NewClientset())
	h := srv.Handler()

	for _, name := range []string{"app-a", "app-b"} {
		doRequest(h, http.MethodPost, "/api/apps", map[string]any{
			"name":      name,
			"namespace": "default",
			"spec": map[string]any{
				"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
			},
		})
	}

	w := doRequest(h, http.MethodGet, "/api/apps?namespace=default", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list apps: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var apps []mortisev1alpha1.App
	json.NewDecoder(w.Body).Decode(&apps)
	if len(apps) < 2 {
		t.Errorf("expected at least 2 apps, got %d", len(apps))
	}
}

func TestUpdateApp(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := api.NewServer(k8sClient, fake.NewClientset())
	h := srv.Handler()

	doRequest(h, http.MethodPost, "/api/apps", map[string]any{
		"name":      "update-me",
		"namespace": "default",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	w := doRequest(h, http.MethodPut, "/api/apps/update-me?namespace=default", map[string]any{
		"source": map[string]any{"type": "image", "image": "nginx:1.26.0"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update app: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var app mortisev1alpha1.App
	json.NewDecoder(w.Body).Decode(&app)
	if app.Spec.Source.Image != "nginx:1.26.0" {
		t.Errorf("expected image nginx:1.26.0, got %s", app.Spec.Source.Image)
	}
}

func TestDeleteApp(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := api.NewServer(k8sClient, fake.NewClientset())
	h := srv.Handler()

	doRequest(h, http.MethodPost, "/api/apps", map[string]any{
		"name":      "delete-me",
		"namespace": "default",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	w := doRequest(h, http.MethodDelete, "/api/apps/delete-me?namespace=default", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delete app: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify deleted
	w = doRequest(h, http.MethodGet, "/api/apps/delete-me?namespace=default", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", w.Code)
	}
}

func TestDeploy(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := api.NewServer(k8sClient, fake.NewClientset())
	h := srv.Handler()

	doRequest(h, http.MethodPost, "/api/apps", map[string]any{
		"name":      "deploy-target",
		"namespace": "default",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	w := doRequest(h, http.MethodPost, "/api/deploy", map[string]any{
		"app":       "deploy-target",
		"namespace": "default",
		"image":     "nginx:1.26.0",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("deploy: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify image was updated on the CRD
	var app mortisev1alpha1.App
	err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "deploy-target", Namespace: "default"}, &app)
	if err != nil {
		t.Fatalf("get app after deploy: %v", err)
	}
	if app.Spec.Source.Image != "nginx:1.26.0" {
		t.Errorf("expected image nginx:1.26.0, got %s", app.Spec.Source.Image)
	}
}

func TestSecretsCRUD(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := api.NewServer(k8sClient, fake.NewClientset())
	h := srv.Handler()

	// Create a secret for an app
	w := doRequest(h, http.MethodPost, "/api/apps/myapp/secrets?namespace=default", map[string]any{
		"name": "db-creds",
		"data": map[string]string{"password": "s3cret"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create secret: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// List secrets
	w = doRequest(h, http.MethodGet, "/api/apps/myapp/secrets?namespace=default", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list secrets: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var secrets []map[string]any
	json.NewDecoder(w.Body).Decode(&secrets)
	if len(secrets) != 1 {
		t.Fatalf("expected 1 secret, got %d", len(secrets))
	}
	if secrets[0]["name"] != "db-creds" {
		t.Errorf("expected secret name db-creds, got %v", secrets[0]["name"])
	}

	// Delete secret
	w = doRequest(h, http.MethodDelete, "/api/apps/myapp/secrets/db-creds?namespace=default", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delete secret: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify deleted
	w = doRequest(h, http.MethodGet, "/api/apps/myapp/secrets?namespace=default", nil)
	json.NewDecoder(w.Body).Decode(&secrets)
	if len(secrets) != 0 {
		t.Errorf("expected 0 secrets after delete, got %d", len(secrets))
	}
}

func TestUnauthenticatedRequest(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := api.NewServer(k8sClient, fake.NewClientset())
	h := srv.Handler()

	// Request without Authorization header
	req := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestGetAppNotFound(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := api.NewServer(k8sClient, fake.NewClientset())
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/apps/nonexistent?namespace=default", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}
