package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

// hostnameRegex validates a bare hostname (no scheme, no port).
var hostnameRegex = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`)

type domainsResponse struct {
	Primary string   `json:"primary"`
	Custom  []string `json:"custom"`
}

type addDomainRequest struct {
	Domain string `json:"domain"`
}

// ListDomains returns the primary and custom domains for an app's environment.
//
// GET /api/projects/{project}/apps/{app}/domains?environment=production
func (s *Server) ListDomains(w http.ResponseWriter, r *http.Request) {
	ns, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	appName := chi.URLParam(r, "app")
	envName := r.URL.Query().Get("environment")
	if envName == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"environment query parameter is required"})
		return
	}

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	env := findEnvironment(&app, envName)
	if env == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{"environment not found"})
		return
	}

	writeJSON(w, http.StatusOK, domainsResponse{
		Primary: env.Domain,
		Custom:  env.CustomDomains,
	})
}

// AddDomain appends a custom domain to an app's environment.
//
// POST /api/projects/{project}/apps/{app}/domains?environment=production
func (s *Server) AddDomain(w http.ResponseWriter, r *http.Request) {
	ns, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	appName := chi.URLParam(r, "app")
	envName := r.URL.Query().Get("environment")
	if envName == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"environment query parameter is required"})
		return
	}

	var req addDomainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if !hostnameRegex.MatchString(req.Domain) {
		writeJSON(w, http.StatusBadRequest, errorResponse{"domain must be a valid hostname (e.g. app.example.com)"})
		return
	}

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	env := findEnvironment(&app, envName)
	if env == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{"environment not found"})
		return
	}

	// Reject duplicates.
	if slices.Contains(env.CustomDomains, req.Domain) {
		writeJSON(w, http.StatusConflict, errorResponse{"domain already exists"})
		return
	}

	env.CustomDomains = append(env.CustomDomains, req.Domain)
	if err := s.client.Update(r.Context(), &app); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, domainsResponse{
		Primary: env.Domain,
		Custom:  env.CustomDomains,
	})
}

// RemoveDomain removes a custom domain from an app's environment.
//
// DELETE /api/projects/{project}/apps/{app}/domains/{domain}?environment=production
func (s *Server) RemoveDomain(w http.ResponseWriter, r *http.Request) {
	ns, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	appName := chi.URLParam(r, "app")
	envName := r.URL.Query().Get("environment")
	domain := chi.URLParam(r, "domain")
	if envName == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"environment query parameter is required"})
		return
	}

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	env := findEnvironment(&app, envName)
	if env == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{"environment not found"})
		return
	}

	idx := slices.Index(env.CustomDomains, domain)
	if idx < 0 {
		writeJSON(w, http.StatusNotFound, errorResponse{"domain not found"})
		return
	}

	env.CustomDomains = slices.Delete(env.CustomDomains, idx, idx+1)
	if err := s.client.Update(r.Context(), &app); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, domainsResponse{
		Primary: env.Domain,
		Custom:  env.CustomDomains,
	})
}

// findEnvironment returns a pointer to the named environment inside the App
// spec, or nil if not found. The pointer is into the App's slice so mutations
// are reflected on the App.
func findEnvironment(app *mortisev1alpha1.App, name string) *mortisev1alpha1.Environment {
	for i := range app.Spec.Environments {
		if app.Spec.Environments[i].Name == name {
			return &app.Spec.Environments[i]
		}
	}
	return nil
}
