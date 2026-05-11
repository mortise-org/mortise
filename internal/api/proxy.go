package api

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
)

var proxyListen = net.Listen

type retryTransport struct {
	base     http.RoundTripper
	retries  int
	backoffs []time.Duration
}

func newRetryTransport(base http.RoundTripper) *retryTransport {
	return &retryTransport{
		base:     base,
		retries:  3,
		backoffs: []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, 1 * time.Second},
	}
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	canRetry := req.Body == nil || req.GetBody != nil

	resp, err := t.base.RoundTrip(req)
	if err == nil || !canRetry || !isTransientConnError(err) {
		return resp, err
	}

	for i := 0; i < t.retries; i++ {
		time.Sleep(t.backoffs[i])

		if req.GetBody != nil {
			body, bodyErr := req.GetBody()
			if bodyErr != nil {
				return nil, err
			}
			req.Body = body
		}

		resp, err = t.base.RoundTrip(req)
		if err == nil || !isTransientConnError(err) {
			return resp, err
		}
	}
	return nil, err
}

func isTransientConnError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

// appProxyEntry tracks a running per-app proxy listener.
type appProxyEntry struct {
	Port     int    `json:"port"`
	URL      string `json:"url"`
	listener net.Listener
}

type proxyPending struct {
	done chan struct{}
}

const proxyBindAddress = "127.0.0.1"

// appProxyManager manages per-app reverse proxy listeners. Each app gets its
// own port — no path prefix, no asset rewriting issues.
type appProxyManager struct {
	mu      sync.Mutex
	proxies map[string]*appProxyEntry // key: "project/app/env"
	pending map[string]*proxyPending
}

func proxyKey(project, app, env string) string {
	return project + "/" + app + "/" + env
}

func newAppProxyManager() *appProxyManager {
	return &appProxyManager{
		proxies: make(map[string]*appProxyEntry),
		pending: make(map[string]*proxyPending),
	}
}

// handleConnect starts a reverse proxy listener for an app on an auto-allocated
// port and returns the URL. If already running, returns the existing URL.
// Both the CLI and UI call this same endpoint.
//
// POST /api/projects/{project}/apps/{app}/connect
//
// @Summary Connect to an app via local proxy
// @Description Start a reverse proxy listener on an auto-allocated local port for the app. Returns the existing proxy URL if already running.
// @Tags proxy
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Param environment query string false "Environment name (defaults to first env)"
// @Success 200 {object} appProxyEntry
// @Failure 404 {object} errorResponse
// @Failure 500 {object} errorResponse
// @Router /projects/{project}/apps/{app}/connect [post]
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionRead) {
		return
	}
	log := logf.FromContext(r.Context())
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	appName := chi.URLParam(r, "app")
	env := envFromQuery(r)
	envNs := constants.EnvNamespace(projectName, env)
	key := proxyKey(projectName, appName, env)

	for {
		s.proxies.mu.Lock()
		if entry, exists := s.proxies.proxies[key]; exists {
			s.proxies.mu.Unlock()
			writeJSON(w, http.StatusOK, entry)
			return
		}
		if pending, exists := s.proxies.pending[key]; exists {
			s.proxies.mu.Unlock()
			select {
			case <-pending.done:
				continue
			case <-r.Context().Done():
				writeJSON(w, http.StatusRequestTimeout, errorResponse{"request canceled while waiting for proxy"})
				return
			}
		}
		pending := &proxyPending{done: make(chan struct{})}
		s.proxies.pending[key] = pending
		s.proxies.mu.Unlock()
		entry, err := s.createProxyEntry(r, ns, appName, envNs, key, log)
		s.proxies.mu.Lock()
		if err == nil {
			s.proxies.proxies[key] = entry
		}
		delete(s.proxies.pending, key)
		close(pending.done)
		s.proxies.mu.Unlock()
		if err != nil {
			var status int
			var msg string
			switch {
			case errors.Is(err, errProxyAppNotFound):
				status = http.StatusNotFound
				msg = "app not found"
			case errors.Is(err, errProxyInvalidTarget):
				status = http.StatusInternalServerError
				msg = "invalid proxy target"
			default:
				status = http.StatusInternalServerError
				msg = err.Error()
			}
			writeJSON(w, status, errorResponse{msg})
			return
		}
		log.Info("started app proxy", "app", key, "port", entry.Port, "target", entry.URL)
		writeJSON(w, http.StatusOK, entry)
		return
	}
}

var (
	errProxyAppNotFound   = errors.New("app not found")
	errProxyInvalidTarget = errors.New("invalid proxy target")
)

func (s *Server) createProxyEntry(r *http.Request, ns, appName, envNs, key string, log interface{ Info(string, ...any) }) (*appProxyEntry, error) {
	// Resolve app CRD (control ns) to get the port.
	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
		return nil, errProxyAppNotFound
	}

	port := app.Spec.Network.Port
	if port == 0 {
		port = 8080
	}

	target := fmt.Sprintf("http://%s.%s.svc:%d", appName, envNs, port)
	targetURL, err := url.Parse(target)
	if err != nil {
		return nil, errProxyInvalidTarget
	}

	// Allocate a random port and start listening.
	listener, err := proxyListen("tcp", proxyBindAddress+":0")
	if err != nil {
		return nil, fmt.Errorf("failed to allocate port: %w", err)
	}
	allocatedPort := listener.Addr().(*net.TCPAddr).Port

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.Host = targetURL.Host
		},
		Transport: newRetryTransport(&http.Transport{
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			ResponseHeaderTimeout: 30 * time.Second,
		}),
	}

	// Start serving in the background.
	go func() {
		if err := http.Serve(listener, proxy); err != nil {
			log.Info("app proxy stopped", "app", key, "error", err)
		}
	}()

	return &appProxyEntry{
		Port:     allocatedPort,
		URL:      fmt.Sprintf("http://localhost:%d", allocatedPort),
		listener: listener,
	}, nil
}

// handleDisconnect stops the proxy for an app.
//
// POST /api/projects/{project}/apps/{app}/disconnect
//
// @Summary Disconnect an app's local proxy
// @Description Stop the reverse proxy listener for an app and free the allocated port.
// @Tags proxy
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Success 204
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app}/disconnect [post]
func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionRead) {
		return
	}
	appName := chi.URLParam(r, "app")
	env := envFromQuery(r)
	key := proxyKey(projectName, appName, env)

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
