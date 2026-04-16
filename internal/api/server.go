package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Server is the REST API server that translates HTTP requests into CRD operations.
type Server struct {
	client client.Client
}

// NewServer creates a new API server backed by the given controller-runtime client.
func NewServer(c client.Client) *Server {
	return &Server{client: c}
}

// Handler returns the root HTTP handler with all routes mounted.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(JWTAuth)

	r.Route("/api", func(r chi.Router) {
		r.Post("/apps", s.CreateApp)
		r.Get("/apps", s.ListApps)
		r.Get("/apps/{name}", s.GetApp)
		r.Put("/apps/{name}", s.UpdateApp)
		r.Delete("/apps/{name}", s.DeleteApp)

		r.Post("/deploy", s.Deploy)

		r.Post("/apps/{name}/secrets", s.CreateSecret)
		r.Get("/apps/{name}/secrets", s.ListSecrets)
		r.Delete("/apps/{name}/secrets/{secretName}", s.DeleteSecret)
	})

	return r
}
