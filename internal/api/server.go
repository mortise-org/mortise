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
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()

	// Unauthenticated API routes (auth endpoints)
	r.Post("/api/auth/setup", s.Setup)
	r.Post("/api/auth/login", s.Login)

	// Authenticated API routes
	r.Route("/api", func(r chi.Router) {
		r.Use(s.jwtAuthMiddleware)

		r.Post("/apps", s.CreateApp)
		r.Get("/apps", s.ListApps)
		r.Get("/apps/{name}", s.GetApp)
		r.Put("/apps/{name}", s.UpdateApp)
		r.Delete("/apps/{name}", s.DeleteApp)

		r.Post("/deploy", s.Deploy)

		r.Post("/apps/{name}/secrets", s.CreateSecret)
		r.Get("/apps/{name}/secrets", s.ListSecrets)
		r.Delete("/apps/{name}/secrets/{secretName}", s.DeleteSecret)

		r.Get("/apps/{name}/logs", s.handleLogs)
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
