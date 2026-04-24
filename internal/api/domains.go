package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"

	"github.com/go-chi/chi/v5"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
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
// Returns an empty payload (not 404) when the App has no override for this
// env -- every App auto-participates in every project env, and overrides only
// exist when the user has customized something.
//
// GET /api/projects/{project}/apps/{app}/domains?environment=production
//
// @Summary List domains for an app
// @Description Return the primary and custom domains for an app's environment. Returns an empty payload when no override exists.
// @Tags domains
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Param environment query string false "Environment name"
// @Success 200 {object} domainsResponse
// @Router /projects/{project}/apps/{app}/domains [get]
func (s *Server) ListDomains(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionRead) {
		return
	}
	app, envName, ok := s.resolveAppEnv(w, r)
	if !ok {
		return
	}

	env := findEnvironment(app, envName)
	if env == nil {
		writeJSON(w, http.StatusOK, domainsResponse{})
		return
	}

	writeJSON(w, http.StatusOK, domainsResponse{
		Primary: env.Domain,
		Custom:  env.CustomDomains,
	})
}

// AddDomain appends a custom domain to an app's environment. Auto-creates
// the App's override entry for this env when it doesn't already exist.
//
// POST /api/projects/{project}/apps/{app}/domains?environment=production
//
// @Summary Add a custom domain
// @Description Append a custom domain to an app's environment. Auto-creates the environment override entry if it does not exist.
// @Tags domains
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Param environment query string false "Environment name"
// @Param body body addDomainRequest true "Domain to add"
// @Success 200 {object} domainsResponse
// @Failure 400 {object} errorResponse
// @Failure 409 {object} errorResponse
// @Router /projects/{project}/apps/{app}/domains [post]
func (s *Server) AddDomain(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionUpdate) {
		return
	}
	app, envName, ok := s.resolveAppEnv(w, r)
	if !ok {
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

	env := ensureEnvironment(app, envName)

	if slices.Contains(env.CustomDomains, req.Domain) {
		writeJSON(w, http.StatusConflict, errorResponse{"domain already exists"})
		return
	}

	env.CustomDomains = append(env.CustomDomains, req.Domain)
	if err := s.client.Update(r.Context(), app); err != nil {
		writeError(w, err)
		return
	}

	s.recordActivity(r, projectName, "update", "domain", req.Domain, "Added custom domain "+req.Domain+" to "+app.Name+" in "+envName, "")

	writeJSON(w, http.StatusOK, domainsResponse{
		Primary: env.Domain,
		Custom:  env.CustomDomains,
	})
}

// RemoveDomain removes a custom domain from an app's environment.
//
// DELETE /api/projects/{project}/apps/{app}/domains/{domain}?environment=production
//
// @Summary Remove a custom domain
// @Description Remove a custom domain from an app's environment.
// @Tags domains
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Param domain path string true "Domain to remove"
// @Param environment query string false "Environment name"
// @Success 200 {object} domainsResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app}/domains/{domain} [delete]
func (s *Server) RemoveDomain(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionUpdate) {
		return
	}
	app, envName, ok := s.resolveAppEnv(w, r)
	if !ok {
		return
	}
	domain := chi.URLParam(r, "domain")

	env := findEnvironment(app, envName)
	if env == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{"domain not found"})
		return
	}

	idx := slices.Index(env.CustomDomains, domain)
	if idx < 0 {
		writeJSON(w, http.StatusNotFound, errorResponse{"domain not found"})
		return
	}

	env.CustomDomains = slices.Delete(env.CustomDomains, idx, idx+1)
	if err := s.client.Update(r.Context(), app); err != nil {
		writeError(w, err)
		return
	}

	s.recordActivity(r, projectName, "update", "domain", domain, "Removed custom domain "+domain+" from "+app.Name+" in "+envName, "")

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
