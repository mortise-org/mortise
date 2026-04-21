package api

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/constants"
)

// appProxyEntry tracks a running per-app proxy listener.
type appProxyEntry struct {
	Port     int    `json:"port"`
	URL      string `json:"url"`
	listener net.Listener
}

// appProxyManager manages per-app reverse proxy listeners. Each app gets its
// own port — no path prefix, no asset rewriting issues.
type appProxyManager struct {
	mu      sync.Mutex
	proxies map[string]*appProxyEntry // key: "project/app"
}

func newAppProxyManager() *appProxyManager {
	return &appProxyManager{proxies: make(map[string]*appProxyEntry)}
}

// handleConnect starts a reverse proxy listener for an app on an auto-allocated
// port and returns the URL. If already running, returns the existing URL.
// Both the CLI and UI call this same endpoint.
//
// POST /api/projects/{project}/apps/{app}/connect
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	log := logf.FromContext(r.Context())
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	appName := chi.URLParam(r, "app")
	key := projectName + "/" + appName

	env := r.URL.Query().Get("env")
	if env == "" {
		env = "production"
	}
	envNs := constants.EnvNamespace(projectName, env)

	// If already proxying, return the existing URL.
	s.proxies.mu.Lock()
	if entry, exists := s.proxies.proxies[key]; exists {
		s.proxies.mu.Unlock()
		writeJSON(w, http.StatusOK, entry)
		return
	}
	s.proxies.mu.Unlock()

	// Resolve app CRD (control ns) to get the port.
	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{"app not found"})
		return
	}

	port := app.Spec.Network.Port
	if port == 0 {
		port = 8080
	}

	svcName := appName + "-" + env
	target := fmt.Sprintf("http://%s.%s.svc:%d", svcName, envNs, port)
	targetURL, err := url.Parse(target)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"invalid proxy target"})
		return
	}

	// Allocate a random port and start listening.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"failed to allocate port: " + err.Error()})
		return
	}
	allocatedPort := listener.Addr().(*net.TCPAddr).Port

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.Host = targetURL.Host
		},
	}

	// Start serving in the background.
	go func() {
		if err := http.Serve(listener, proxy); err != nil {
			log.Info("app proxy stopped", "app", key, "error", err)
		}
	}()

	entry := &appProxyEntry{
		Port:     allocatedPort,
		URL:      fmt.Sprintf("http://localhost:%d", allocatedPort),
		listener: listener,
	}

	s.proxies.mu.Lock()
	s.proxies.proxies[key] = entry
	s.proxies.mu.Unlock()

	log.Info("started app proxy", "app", key, "port", allocatedPort, "target", target)
	writeJSON(w, http.StatusOK, entry)
}

// handleDisconnect stops the proxy for an app.
//
// POST /api/projects/{project}/apps/{app}/disconnect
func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	appName := chi.URLParam(r, "app")
	key := projectName + "/" + appName

	s.proxies.mu.Lock()
	entry, exists := s.proxies.proxies[key]
	if exists {
		entry.listener.Close()
		delete(s.proxies.proxies, key)
	}
	s.proxies.mu.Unlock()

	if !exists {
		writeJSON(w, http.StatusNotFound, errorResponse{"no active proxy for this app"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
