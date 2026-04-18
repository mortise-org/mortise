package api

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

// handleProxy reverse-proxies HTTP requests to an app's in-cluster Service.
// This lets users access apps via the Mortise UI/API without needing separate
// port-forwards or ingress — one port, all apps.
//
// GET /proxy/{project}/{app}/*
func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	appName := chi.URLParam(r, "app")

	// Resolve project namespace.
	var project mortisev1alpha1.Project
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: projectName}, &project); err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	ns := project.Status.Namespace
	if ns == "" {
		ns = "project-" + projectName
	}

	// Resolve app to get the port.
	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}

	port := app.Spec.Network.Port
	if port == 0 {
		port = 8080
	}

	// The service name follows the pattern {app}-{env}. Default to production.
	svcName := appName + "-production"
	target := fmt.Sprintf("http://%s.%s.svc:%d", svcName, ns, port)

	targetURL, err := url.Parse(target)
	if err != nil {
		http.Error(w, "invalid proxy target", http.StatusInternalServerError)
		return
	}

	// Strip the /proxy/{project}/{app} prefix before forwarding.
	prefix := fmt.Sprintf("/proxy/%s/%s", projectName, appName)
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.URL.Path = strings.TrimPrefix(req.URL.Path, prefix)
			if req.URL.Path == "" {
				req.URL.Path = "/"
			}
			req.Host = targetURL.Host
		},
	}

	proxy.ServeHTTP(w, r)
}

// proxyURL returns the proxy URL for an app, for use by the UI and CLI.
func (s *Server) handleProxyURL(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	appName := chi.URLParam(r, "app")

	// Build the proxy URL relative to the current host.
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	proxyURL := fmt.Sprintf("%s://%s/proxy/%s/%s/", scheme, r.Host, projectName, appName)

	writeJSON(w, http.StatusOK, map[string]string{"url": proxyURL})
}
