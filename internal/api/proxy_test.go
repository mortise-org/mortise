package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/auth"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
)

func TestProxyKeyIncludesEnv(t *testing.T) {
	k1 := proxyKey("myproject", "myapp", "staging")
	k2 := proxyKey("myproject", "myapp", "production")

	if k1 == k2 {
		t.Errorf("keys for different environments must differ: staging=%q production=%q", k1, k2)
	}

	pm := newAppProxyManager()
	pm.proxies[k1] = &appProxyEntry{Port: 1234, URL: "http://localhost:1234"}

	if _, ok := pm.proxies[k1]; !ok {
		t.Error("expected staging proxy to exist")
	}
	if _, ok := pm.proxies[k2]; ok {
		t.Error("production proxy should not exist")
	}
}

func TestProxyBindAddressIsLoopback(t *testing.T) {
	ip := net.ParseIP(proxyBindAddress)
	if ip == nil {
		t.Fatalf("proxyBindAddress %q is not a valid IP", proxyBindAddress)
	}
	if !ip.IsLoopback() {
		t.Errorf("proxyBindAddress must be loopback, got %s", proxyBindAddress)
	}
}

type allowAllPolicy struct{}

func (allowAllPolicy) Authorize(context.Context, auth.Principal, authz.Resource, authz.Action) (bool, error) {
	return true, nil
}

func newProxyTestServer(t *testing.T, objs ...runtime.Object) *Server {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}

	return NewServer(fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build(), nil, nil, nil, nil, nil, nil, allowAllPolicy{})
}

func proxyRequest(method, target, project, app string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("project", project)
	routeCtx.URLParams.Add("app", app)

	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, principalKey, &auth.Principal{Email: "test@example.com", Role: auth.RoleAdmin})
	return req.WithContext(ctx)
}

func seedProxyProject(project string, envs ...string) *mortisev1alpha1.Project {
	specEnvs := make([]mortisev1alpha1.ProjectEnvironment, 0, len(envs))
	for i, env := range envs {
		specEnvs = append(specEnvs, mortisev1alpha1.ProjectEnvironment{Name: env, DisplayOrder: i})
	}
	return &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: project},
		Spec:       mortisev1alpha1.ProjectSpec{Environments: specEnvs},
	}
}

func seedProxyApp(project, name string) *mortisev1alpha1.App {
	return &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: constants.ControlNamespace(project),
		},
		Spec: mortisev1alpha1.AppSpec{
			Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
			Network: mortisev1alpha1.NetworkConfig{Port: 8080},
		},
	}
}

func closeProxyListeners(srv *Server) {
	srv.proxies.mu.Lock()
	defer srv.proxies.mu.Unlock()
	for key, entry := range srv.proxies.proxies {
		_ = entry.listener.Close()
		delete(srv.proxies.proxies, key)
	}
}

func TestHandleConnectRejectsUndeclaredEnvironment(t *testing.T) {
	srv := newProxyTestServer(t, seedProxyProject("default", "production"), seedProxyApp("default", "web"))

	w := httptest.NewRecorder()
	srv.handleConnect(w, proxyRequest(http.MethodPost, "/api/projects/default/apps/web/connect?env=staging", "default", "web"))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleConnectRejectsDisabledAppEnvironment(t *testing.T) {
	disabled := false
	app := seedProxyApp("default", "web")
	app.Spec.Environments = []mortisev1alpha1.Environment{{Name: "staging", Enabled: &disabled}}
	srv := newProxyTestServer(t, seedProxyProject("default", "staging", "production"), app)

	w := httptest.NewRecorder()
	srv.handleConnect(w, proxyRequest(http.MethodPost, "/api/projects/default/apps/web/connect?env=staging", "default", "web"))

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
	if len(srv.proxies.proxies) != 0 {
		t.Fatalf("expected no proxy to be created, got %#v", srv.proxies.proxies)
	}
}

func TestHandleDisconnectRejectsUndeclaredEnvironment(t *testing.T) {
	srv := newProxyTestServer(t, seedProxyProject("default", "production"))

	w := httptest.NewRecorder()
	srv.handleDisconnect(w, proxyRequest(http.MethodPost, "/api/projects/default/apps/web/disconnect?env=staging", "default", "web"))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleConnectDefaultsToFirstProjectEnvironment(t *testing.T) {
	srv := newProxyTestServer(t, seedProxyProject("default", "staging", "production"), seedProxyApp("default", "web"))
	t.Cleanup(func() { closeProxyListeners(srv) })

	w := httptest.NewRecorder()
	srv.handleConnect(w, proxyRequest(http.MethodPost, "/api/projects/default/apps/web/connect", "default", "web"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if _, ok := srv.proxies.proxies[proxyKey("default", "web", "staging")]; !ok {
		t.Fatalf("expected staging proxy to be created, got keys %#v", srv.proxies.proxies)
	}
	if _, ok := srv.proxies.proxies[proxyKey("default", "web", "production")]; ok {
		t.Fatalf("did not expect production proxy when env is omitted")
	}
}

func TestHandleDisconnectDefaultsToFirstProjectEnvironment(t *testing.T) {
	srv := newProxyTestServer(t, seedProxyProject("default", "staging", "production"))
	listener, err := net.Listen("tcp", proxyBindAddress+":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		closeProxyListeners(srv)
	})
	srv.proxies.proxies[proxyKey("default", "web", "staging")] = &appProxyEntry{
		Port:     listener.Addr().(*net.TCPAddr).Port,
		URL:      "http://localhost:test",
		listener: listener,
	}

	w := httptest.NewRecorder()
	srv.handleDisconnect(w, proxyRequest(http.MethodPost, "/api/projects/default/apps/web/disconnect", "default", "web"))

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
	if _, ok := srv.proxies.proxies[proxyKey("default", "web", "staging")]; ok {
		t.Fatalf("expected staging proxy to be removed")
	}
}

func TestHandleConnectAndDisconnectAreScopedPerEnvironment(t *testing.T) {
	srv := newProxyTestServer(t, seedProxyProject("default", "staging", "production"), seedProxyApp("default", "web"))
	t.Cleanup(func() { closeProxyListeners(srv) })

	staging := httptest.NewRecorder()
	srv.handleConnect(staging, proxyRequest(http.MethodPost, "/api/projects/default/apps/web/connect?env=staging", "default", "web"))
	if staging.Code != http.StatusOK {
		t.Fatalf("staging connect: expected 200, got %d: %s", staging.Code, staging.Body.String())
	}

	production := httptest.NewRecorder()
	srv.handleConnect(production, proxyRequest(http.MethodPost, "/api/projects/default/apps/web/connect?env=production", "default", "web"))
	if production.Code != http.StatusOK {
		t.Fatalf("production connect: expected 200, got %d: %s", production.Code, production.Body.String())
	}

	var stagingResp, productionResp appProxyEntry
	if err := json.NewDecoder(staging.Body).Decode(&stagingResp); err != nil {
		t.Fatalf("decode staging response: %v", err)
	}
	if err := json.NewDecoder(production.Body).Decode(&productionResp); err != nil {
		t.Fatalf("decode production response: %v", err)
	}
	if stagingResp.URL == productionResp.URL {
		t.Fatalf("expected distinct URLs per env, both were %q", stagingResp.URL)
	}

	disconnect := httptest.NewRecorder()
	srv.handleDisconnect(disconnect, proxyRequest(http.MethodPost, "/api/projects/default/apps/web/disconnect?env=staging", "default", "web"))
	if disconnect.Code != http.StatusNoContent {
		t.Fatalf("staging disconnect: expected 204, got %d: %s", disconnect.Code, disconnect.Body.String())
	}
	if _, ok := srv.proxies.proxies[proxyKey("default", "web", "staging")]; ok {
		t.Fatalf("expected staging proxy to be removed")
	}
	if _, ok := srv.proxies.proxies[proxyKey("default", "web", "production")]; !ok {
		t.Fatalf("expected production proxy to remain active")
	}
	if host := productionResp.URL; host == "" {
		t.Fatal("expected returned proxy URL")
	}
}

func TestHandleDisconnectRemovesStaleProxyAfterProjectEnvChange(t *testing.T) {
	project := seedProxyProject("default", "staging", "production")
	srv := newProxyTestServer(t, project, seedProxyApp("default", "web"))
	listener, err := net.Listen("tcp", proxyBindAddress+":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		closeProxyListeners(srv)
	})
	srv.proxies.proxies[proxyKey("default", "web", "staging")] = &appProxyEntry{
		Port:     listener.Addr().(*net.TCPAddr).Port,
		URL:      "http://localhost:test",
		listener: listener,
	}

	project.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{{Name: "production", DisplayOrder: 0}}
	if err := srv.client.Update(context.Background(), project); err != nil {
		t.Fatalf("update project environments: %v", err)
	}

	w := httptest.NewRecorder()
	srv.handleDisconnect(w, proxyRequest(http.MethodPost, "/api/projects/default/apps/web/disconnect?env=staging", "default", "web"))

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
	if _, ok := srv.proxies.proxies[proxyKey("default", "web", "staging")]; ok {
		t.Fatal("expected stale staging proxy to be removed")
	}
}
