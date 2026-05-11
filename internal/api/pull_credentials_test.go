package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mortise-org/mortise/internal/constants"
)

func TestPullCredentialsCRUDAndDeleteAppDefersCleanupToController(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default", "staging", "production")

	create := doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "web",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create app: expected 201, got %d: %s", create.Code, create.Body.String())
	}

	set := doRequest(h, http.MethodPost, "/api/projects/default/apps/web/pull-credentials", map[string]any{
		"registry": "ghcr.io",
		"username": "octo",
		"password": "secret",
	})
	if set.Code != http.StatusOK {
		t.Fatalf("set pull credentials: expected 200, got %d: %s", set.Code, set.Body.String())
	}

	for _, env := range []string{"staging", "production"} {
		var secret corev1.Secret
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      "web-pull-secret",
			Namespace: constants.EnvNamespace("default", env),
		}, &secret)
		if err != nil {
			t.Fatalf("get pull secret for %s: %v", env, err)
		}
		if secret.Labels[constants.ProjectLabel] != "default" {
			t.Fatalf("pull secret project label = %q, want default", secret.Labels[constants.ProjectLabel])
		}
		if secret.Labels[constants.EnvironmentLabel] != env {
			t.Fatalf("pull secret env label = %q, want %s", secret.Labels[constants.EnvironmentLabel], env)
		}
	}

	get := doRequest(h, http.MethodGet, "/api/projects/default/apps/web/pull-credentials", nil)
	if get.Code != http.StatusOK {
		t.Fatalf("get pull credentials: expected 200, got %d: %s", get.Code, get.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(get.Body).Decode(&resp)
	if resp["registry"] != "ghcr.io" || resp["username"] != "octo" {
		t.Fatalf("unexpected pull credentials response: %+v", resp)
	}

	del := doRequest(h, http.MethodDelete, "/api/projects/default/apps/web", nil)
	if del.Code != http.StatusOK {
		t.Fatalf("delete app: expected 200, got %d: %s", del.Code, del.Body.String())
	}

	for _, env := range []string{"staging", "production"} {
		var secret corev1.Secret
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      "web-pull-secret",
			Namespace: constants.EnvNamespace("default", env),
		}, &secret)
		if err != nil {
			t.Fatalf("expected pull secret for %s to remain until controller GC, got %v", env, err)
		}
	}
}

func TestSetPullCredentialsRejectsUserManagedReservedSecret(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	const projectName = "reserved-secret-conflict"
	seedProject(t, k8sClient, projectName)

	create := doRequest(h, http.MethodPost, "/api/projects/"+projectName+"/apps", map[string]any{
		"name": "web",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create app: expected 201, got %d: %s", create.Code, create.Body.String())
	}

	userSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-pull-secret",
			Namespace: constants.EnvNamespace(projectName, "production"),
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{".dockerconfigjson": []byte(`{"auths":{"example.com":{"username":"user","password":"pw"}}}`)},
	}
	if err := k8sClient.Create(context.Background(), userSecret); err != nil {
		t.Fatalf("create user-managed secret: %v", err)
	}

	set := doRequest(h, http.MethodPost, "/api/projects/"+projectName+"/apps/web/pull-credentials", map[string]any{
		"registry": "ghcr.io",
		"username": "octo",
		"password": "secret",
	})
	if set.Code != http.StatusConflict {
		t.Fatalf("expected 409 for user-managed reserved secret, got %d: %s", set.Code, set.Body.String())
	}

	var secret corev1.Secret
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "web-pull-secret", Namespace: constants.EnvNamespace(projectName, "production")}, &secret); err != nil {
		t.Fatalf("get user-managed secret: %v", err)
	}
	if secret.Labels["app.kubernetes.io/managed-by"] == "mortise" {
		t.Fatal("user-managed secret was unexpectedly adopted")
	}
}

func TestSetPullCredentialsPreflightsAllEnvironmentsBeforeWriting(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	const projectName = "reserved-secret-preflight"
	seedProject(t, k8sClient, projectName, "staging", "production")

	create := doRequest(h, http.MethodPost, "/api/projects/"+projectName+"/apps", map[string]any{
		"name": "web",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create app: expected 201, got %d: %s", create.Code, create.Body.String())
	}

	userSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-pull-secret",
			Namespace: constants.EnvNamespace(projectName, "production"),
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{".dockerconfigjson": []byte(`{"auths":{"example.com":{"username":"user","password":"pw"}}}`)},
	}
	if err := k8sClient.Create(context.Background(), userSecret); err != nil {
		t.Fatalf("create conflicting user-managed secret: %v", err)
	}

	set := doRequest(h, http.MethodPost, "/api/projects/"+projectName+"/apps/web/pull-credentials", map[string]any{
		"registry": "ghcr.io",
		"username": "octo",
		"password": "secret",
	})
	if set.Code != http.StatusConflict {
		t.Fatalf("expected 409 for user-managed reserved secret, got %d: %s", set.Code, set.Body.String())
	}

	for _, env := range []string{"staging", "production"} {
		var secret corev1.Secret
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      "web-pull-secret",
			Namespace: constants.EnvNamespace(projectName, env),
		}, &secret)
		if env == "staging" {
			if !apierrors.IsNotFound(err) {
				t.Fatalf("expected staging secret to remain untouched, got %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("get conflicting production secret: %v", err)
		}
		if secret.Labels["app.kubernetes.io/managed-by"] == "mortise" {
			t.Fatal("user-managed production secret was unexpectedly adopted")
		}
	}

	var app struct {
		Spec struct {
			Source struct {
				PullSecretRef string `json:"pullSecretRef"`
			} `json:"source"`
		} `json:"spec"`
	}
	getApp := doRequest(h, http.MethodGet, "/api/projects/"+projectName+"/apps/web", nil)
	if getApp.Code != http.StatusOK {
		t.Fatalf("get app: expected 200, got %d: %s", getApp.Code, getApp.Body.String())
	}
	if err := json.NewDecoder(getApp.Body).Decode(&app); err != nil {
		t.Fatalf("decode app: %v", err)
	}
	if app.Spec.Source.PullSecretRef != "" {
		t.Fatalf("pullSecretRef = %q, want empty on conflict", app.Spec.Source.PullSecretRef)
	}
}

func TestGetPullCredentialsMissingAppReturns404(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	staleSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ghost-pull-secret",
			Namespace: constants.EnvNamespace("default", "production"),
			Labels: map[string]string{
				constants.AppNameLabel:         "ghost",
				"app.kubernetes.io/managed-by": "mortise",
			},
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{".dockerconfigjson": []byte(`{"auths":{"ghcr.io":{"username":"octo","password":"pw"}}}`)},
	}
	if err := k8sClient.Create(context.Background(), staleSecret); err != nil {
		t.Fatalf("create stale secret: %v", err)
	}

	get := doRequest(h, http.MethodGet, "/api/projects/default/apps/ghost/pull-credentials", nil)
	if get.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing app, got %d: %s", get.Code, get.Body.String())
	}
}
