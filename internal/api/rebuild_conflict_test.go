package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/auth"
	"github.com/mortise-org/mortise/internal/constants"
)

// TestRedeployRetriesConflict proves the mo-e4y fix: a concurrent controller
// write to either the Deployment (restartDeployment) or the App status
// (setEnvsDeploying) must be retried, not surfaced as a user-facing 409. The
// interceptor fires one synthetic conflict on the first Deployment update AND
// one on the first App status update; the handler must still return 200.
func TestRedeployRetriesConflict(t *testing.T) {
	if err := mortisev1alpha1.AddToScheme(scheme.Scheme); err != nil {
		t.Fatal(err)
	}

	ns := constants.ControlNamespace("default")
	envNs := constants.EnvNamespace("default", "production")
	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec:       mortisev1alpha1.ProjectSpec{Environments: []mortisev1alpha1.ProjectEnvironment{{Name: "production"}}},
		Status:     mortisev1alpha1.ProjectStatus{Namespace: ns},
	}
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "redeploy-conflict", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source:       mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.27"},
			Environments: []mortisev1alpha1.Environment{{Name: "production"}},
		},
		Status: mortisev1alpha1.AppStatus{
			Phase: mortisev1alpha1.AppPhaseReady,
			Environments: []mortisev1alpha1.EnvironmentStatus{
				{Name: "production", Phase: mortisev1alpha1.AppPhaseReady, CurrentImage: "nginx:1.27"},
			},
		},
	}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "redeploy-conflict", Namespace: envNs},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "redeploy-conflict"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "redeploy-conflict"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx:1.27"}}},
			},
		},
	}

	depConflictFired, statusConflictFired := false, false
	base := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(project, app, dep).
		WithStatusSubresource(app).
		Build()
	client := interceptor.NewClient(base, interceptor.Funcs{
		Update: func(ctx context.Context, c ctrlclient.WithWatch, obj ctrlclient.Object, opts ...ctrlclient.UpdateOption) error {
			if _, ok := obj.(*appsv1.Deployment); ok && !depConflictFired {
				depConflictFired = true
				return apierrors.NewConflict(appsv1.Resource("deployments"), obj.GetName(), fmt.Errorf("synthetic conflict"))
			}
			return c.Update(ctx, obj, opts...)
		},
		SubResourceUpdate: func(ctx context.Context, c ctrlclient.Client, subResourceName string, obj ctrlclient.Object, opts ...ctrlclient.SubResourceUpdateOption) error {
			if subResourceName == "status" && !statusConflictFired {
				statusConflictFired = true
				return apierrors.NewConflict(mortisev1alpha1.GroupVersion.WithResource("apps").GroupResource(), obj.GetName(), fmt.Errorf("synthetic conflict"))
			}
			return c.Status().Update(ctx, obj, opts...)
		},
	})

	s := &Server{client: client, authz: allowAllAppsPolicy{}}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/default/apps/redeploy-conflict/redeploy?environment=production", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("project", "default")
	routeCtx.URLParams.Add("app", "redeploy-conflict")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, principalKey, &auth.Principal{Email: "test@example.com", Role: auth.RoleAdmin})
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	s.Redeploy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("redeploy under conflict: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !depConflictFired {
		t.Error("expected a synthetic conflict on the Deployment update")
	}
	if !statusConflictFired {
		t.Error("expected a synthetic conflict on the App status update")
	}

	// The retried writes must have landed: annotation stamped, phase Deploying.
	var gotDep appsv1.Deployment
	if err := base.Get(context.Background(), types.NamespacedName{Name: "redeploy-conflict", Namespace: envNs}, &gotDep); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if gotDep.Spec.Template.Annotations["mortise.dev/restartedAt"] == "" {
		t.Error("expected restartedAt annotation after retried Deployment update")
	}
	var gotApp mortisev1alpha1.App
	if err := base.Get(context.Background(), types.NamespacedName{Name: "redeploy-conflict", Namespace: ns}, &gotApp); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if gotApp.Status.Phase != mortisev1alpha1.AppPhaseDeploying {
		t.Errorf("expected phase Deploying after retried status update, got %s", gotApp.Status.Phase)
	}
}

// TestAddDomainRetriesConflict proves the domains.go AddDomain fix: a
// concurrent App spec write is retried rather than surfaced as a 409.
func TestAddDomainRetriesConflict(t *testing.T) {
	if err := mortisev1alpha1.AddToScheme(scheme.Scheme); err != nil {
		t.Fatal(err)
	}

	ns := constants.ControlNamespace("default")
	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec:       mortisev1alpha1.ProjectSpec{Environments: []mortisev1alpha1.ProjectEnvironment{{Name: "production"}}},
		Status:     mortisev1alpha1.ProjectStatus{Namespace: ns},
	}
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "domain-conflict", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source:       mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.27"},
			Environments: []mortisev1alpha1.Environment{{Name: "production"}},
		},
	}

	fired := false
	base := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(project, app).Build()
	s := &Server{client: &appUpdateConflictClient{Client: base, fired: &fired}, authz: allowAllAppsPolicy{}}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/default/apps/domain-conflict/domains?environment=production",
		strings.NewReader(`{"domain":"app.example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("project", "default")
	routeCtx.URLParams.Add("app", "domain-conflict")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, principalKey, &auth.Principal{Email: "test@example.com", Role: auth.RoleAdmin})
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	s.AddDomain(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("add domain under conflict: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !fired {
		t.Error("expected a synthetic conflict on the App update")
	}
	var gotApp mortisev1alpha1.App
	if err := base.Get(context.Background(), types.NamespacedName{Name: "domain-conflict", Namespace: ns}, &gotApp); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if len(gotApp.Spec.Environments) == 0 || !contains(gotApp.Spec.Environments[0].CustomDomains, "app.example.com") {
		t.Errorf("expected custom domain persisted after retry, got %+v", gotApp.Spec.Environments)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
