package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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

func newProxyRaceServer(t *testing.T) *Server {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}

	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: mortisev1alpha1.ProjectSpec{
			Environments: []mortisev1alpha1.ProjectEnvironment{
				{Name: "production", DisplayOrder: 0},
				{Name: "staging", DisplayOrder: 1},
			},
		},
		Status: mortisev1alpha1.ProjectStatus{Namespace: constants.ControlNamespace("default")},
	}
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: constants.ControlNamespace("default")},
		Spec: mortisev1alpha1.AppSpec{
			Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
			Network: mortisev1alpha1.NetworkConfig{Port: 8080},
		},
	}
	apiApp := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: constants.ControlNamespace("default")},
		Spec: mortisev1alpha1.AppSpec{
			Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
			Network: mortisev1alpha1.NetworkConfig{Port: 9090},
		},
	}

	return NewServer(fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(project, app, apiApp).Build(), nil, nil, nil, nil, nil, nil, allowAllPolicy{})
}

func proxyRaceRequest(method, target, app string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("project", "default")
	routeCtx.URLParams.Add("app", app)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, principalKey, &auth.Principal{Email: "test@example.com", Role: auth.RoleAdmin})
	return req.WithContext(ctx)
}

func closeProxyEntries(srv *Server) {
	srv.proxies.mu.Lock()
	defer srv.proxies.mu.Unlock()
	for key, entry := range srv.proxies.proxies {
		_ = entry.listener.Close()
		delete(srv.proxies.proxies, key)
	}
}

func TestHandleConnectCreatesOneListenerPerKey(t *testing.T) {
	srv := newProxyRaceServer(t)
	t.Cleanup(func() { closeProxyEntries(srv) })

	origListen := proxyListen
	var listenCalls atomic.Int32
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var listenersMu sync.Mutex
	var listeners []net.Listener
	proxyListen = func(network, addr string) (net.Listener, error) {
		call := listenCalls.Add(1)
		if call == 1 {
			close(firstEntered)
			<-releaseFirst
		}
		ln, err := origListen(network, addr)
		if err == nil {
			listenersMu.Lock()
			listeners = append(listeners, ln)
			listenersMu.Unlock()
		}
		return ln, err
	}
	defer func() {
		proxyListen = origListen
		listenersMu.Lock()
		defer listenersMu.Unlock()
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	rec1 := httptest.NewRecorder()
	rec2 := httptest.NewRecorder()
	go func() {
		defer wg.Done()
		srv.handleConnect(rec1, proxyRaceRequest(http.MethodPost, "/api/projects/default/apps/web/connect?env=production", "web"))
	}()

	select {
	case <-firstEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first connect did not reach listener creation")
	}

	go func() {
		defer wg.Done()
		srv.handleConnect(rec2, proxyRaceRequest(http.MethodPost, "/api/projects/default/apps/web/connect?env=production", "web"))
	}()

	time.Sleep(150 * time.Millisecond)
	if got := listenCalls.Load(); got != 1 {
		t.Fatalf("expected one listener creation for same key, got %d", got)
	}

	close(releaseFirst)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("connect calls did not complete")
	}

	if rec1.Code != http.StatusOK || rec2.Code != http.StatusOK {
		t.Fatalf("expected both connect calls to return 200, got %d and %d", rec1.Code, rec2.Code)
	}
	var resp1, resp2 appProxyEntry
	if err := json.NewDecoder(rec1.Body).Decode(&resp1); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if err := json.NewDecoder(rec2.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if resp1.URL != resp2.URL {
		t.Fatalf("expected one shared proxy URL, got %q and %q", resp1.URL, resp2.URL)
	}
}

func TestHandleConnectDoesNotSerializeDifferentKeys(t *testing.T) {
	srv := newProxyRaceServer(t)
	t.Cleanup(func() { closeProxyEntries(srv) })

	origListen := proxyListen
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	var listenCalls atomic.Int32
	var listenersMu sync.Mutex
	var listeners []net.Listener
	proxyListen = func(network, addr string) (net.Listener, error) {
		call := listenCalls.Add(1)
		switch call {
		case 1:
			close(firstEntered)
			<-releaseFirst
		case 2:
			close(secondEntered)
		}
		ln, err := origListen(network, addr)
		if err == nil {
			listenersMu.Lock()
			listeners = append(listeners, ln)
			listenersMu.Unlock()
		}
		return ln, err
	}
	defer func() {
		proxyListen = origListen
		listenersMu.Lock()
		defer listenersMu.Unlock()
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}()

	rec1 := httptest.NewRecorder()
	rec2 := httptest.NewRecorder()
	done1 := make(chan struct{})
	done2 := make(chan struct{})
	go func() {
		defer close(done1)
		srv.handleConnect(rec1, proxyRaceRequest(http.MethodPost, "/api/projects/default/apps/web/connect?env=production", "web"))
	}()
	select {
	case <-firstEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first key did not begin listener creation")
	}

	go func() {
		defer close(done2)
		srv.handleConnect(rec2, proxyRaceRequest(http.MethodPost, "/api/projects/default/apps/api/connect?env=staging", "api"))
	}()

	select {
	case <-secondEntered:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("different proxy key was blocked by unrelated in-flight connect")
	}
	if got := listenCalls.Load(); got != 2 {
		t.Fatalf("expected two listener creations for different keys, got %d", got)
	}

	select {
	case <-done2:
	case <-time.After(2 * time.Second):
		t.Fatal("different-key connect did not complete independently")
	}
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected different-key connect to return 200, got %d", rec2.Code)
	}

	close(releaseFirst)
	select {
	case <-done1:
	case <-time.After(2 * time.Second):
		t.Fatal("first connect did not complete after release")
	}
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected first-key connect to return 200, got %d", rec1.Code)
	}
}

func TestHandleDisconnectRemovesStaleProxyAfterProjectEnvChange(t *testing.T) {
	srv := newProxyRaceServer(t)
	listener, err := net.Listen("tcp", proxyBindAddress+":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		closeProxyEntries(srv)
	})
	srv.proxies.proxies[proxyKey("default", "web", "staging")] = &appProxyEntry{
		Port:     listener.Addr().(*net.TCPAddr).Port,
		URL:      "http://localhost:test",
		listener: listener,
	}

	project := &mortisev1alpha1.Project{}
	if err := srv.client.Get(context.Background(), types.NamespacedName{Name: "default"}, project); err != nil {
		t.Fatalf("get project: %v", err)
	}
	project.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{{Name: "production", DisplayOrder: 0}}
	if err := srv.client.Update(context.Background(), project); err != nil {
		t.Fatalf("update project environments: %v", err)
	}

	w := httptest.NewRecorder()
	srv.handleDisconnect(w, proxyRaceRequest(http.MethodPost, "/api/projects/default/apps/web/disconnect?env=staging", "web"))

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
	if _, ok := srv.proxies.proxies[proxyKey("default", "web", "staging")]; ok {
		t.Fatal("expected stale staging proxy to be removed")
	}
}
