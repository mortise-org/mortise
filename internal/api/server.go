package api

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/MC-Meesh/mortise/internal/auth"
)

// Server is the REST API server that translates HTTP requests into CRD operations.
type Server struct {
	client    client.Client
	clientset kubernetes.Interface
	auth      auth.AuthProvider
	jwt       *auth.JWTHelper
	ui        fs.FS
}

// NewServer creates a new API server.
// ui is an optional filesystem for serving the SvelteKit UI; pass nil to disable UI serving.
func NewServer(c client.Client, cs kubernetes.Interface, authProvider auth.AuthProvider, jwt *auth.JWTHelper, ui fs.FS) *Server {
	return &Server{
		client:    c,
		clientset: cs,
		auth:      authProvider,
		jwt:       jwt,
		ui:        ui,
	}
}

// Handler returns the root HTTP handler with all routes mounted.
//
// URL scheme:
//
//	/api/auth/{status|setup|login}                                 unauthenticated
//	/api/projects                                                  list/create
//	/api/projects/{project}                                        get/delete
//	/api/projects/{project}/apps                                   list/create
//	/api/projects/{project}/apps/{app}                             get/update/delete
//	/api/projects/{project}/apps/{app}/deploy                      deploy webhook
//	/api/projects/{project}/apps/{app}/logs                        SSE log stream
//	/api/projects/{project}/apps/{app}/secrets                     list/create
//	/api/projects/{project}/apps/{app}/secrets/{secretName}        delete
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()

	// Unauthenticated auth endpoints.
	r.Get("/api/auth/status", s.Status)
	r.Post("/api/auth/setup", s.Setup)
	r.Post("/api/auth/login", s.Login)

	// Authenticated /api routes.
	r.Route("/api", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(s.jwtAuthMiddleware)

			r.Post("/projects", s.CreateProject)
			r.Get("/projects", s.ListProjects)
			r.Get("/projects/{project}", s.GetProject)
			r.Delete("/projects/{project}", s.DeleteProject)

			r.Post("/projects/{project}/apps", s.CreateApp)
			r.Get("/projects/{project}/apps", s.ListApps)
			r.Get("/projects/{project}/apps/{app}", s.GetApp)
			r.Put("/projects/{project}/apps/{app}", s.UpdateApp)
			r.Delete("/projects/{project}/apps/{app}", s.DeleteApp)

			r.Post("/projects/{project}/apps/{app}/deploy", s.Deploy)

			r.Post("/projects/{project}/apps/{app}/secrets", s.CreateSecret)
			r.Get("/projects/{project}/apps/{app}/secrets", s.ListSecrets)
			r.Delete("/projects/{project}/apps/{app}/secrets/{secretName}", s.DeleteSecret)
		})

		// /logs: JWT may come via `?token=` query param as an EventSource
		// workaround. sseTokenQueryParamMiddleware runs before jwtAuthMiddleware
		// and promotes the query param onto the Authorization header.
		r.Group(func(r chi.Router) {
			r.Use(sseTokenQueryParamMiddleware)
			r.Use(s.jwtAuthMiddleware)
			r.Get("/projects/{project}/apps/{app}/logs", s.handleLogs)
		})
	})

	// UI: serve SvelteKit static files at all non-/api paths
	if s.ui != nil {
		uiHandler := http.FileServer(http.FS(s.ui))
		r.Get("/*", func(w http.ResponseWriter, req *http.Request) {
			// SPA fallback: if the requested file doesn't exist, serve index.html
			path := strings.TrimPrefix(req.URL.Path, "/")
			if path == "" {
				path = "index.html"
			}
			if _, err := fs.Stat(s.ui, path); err != nil {
				req.URL.Path = "/"
			}
			uiHandler.ServeHTTP(w, req)
		})
	}

	return r
}

// UIFS returns a sub-filesystem of the embed.FS rooted at the SvelteKit build directory.
// Pass the result to NewServer as the ui parameter.
func UIFS(embedded embed.FS, subPath string) (fs.FS, error) {
	return fs.Sub(embedded, subPath)
}
