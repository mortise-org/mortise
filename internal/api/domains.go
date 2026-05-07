package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"

	"github.com/go-chi/chi/v5"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
)

// hostnameRegex validates a bare hostname (no scheme, no port).
var hostnameRegex = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`)

type domainsResponse struct {
	Primary string   `json:"primary"`
	Custom  []string `json:"custom"`
	Auto    string   `json:"auto,omitempty"`
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
		writeJSON(w, http.StatusOK, domainsResponse{Auto: statusAutoDomain(app, envName)})
		return
	}

	writeJSON(w, http.StatusOK, domainsResponse{
		Primary: env.Domain,
		Custom:  env.CustomDomains,
		Auto:    statusAutoDomain(app, envName),
	})
}

// AddDomain appends a custom domain to an app's environment. Auto-creates
// the App's override entry for this env when it doesn't already exist.
//
// POST /api/projects/{project}/apps/{app}/domains?environment=production
//
// @Summary Add a custom domain
// @Description Append a custom domain to an app's environment. Validates cross-app uniqueness and returns 409 on collision. Auto-creates the environment override entry if it does not exist.
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

	// Check for cross-app domain collisions before accepting.
	var allApps mortisev1alpha1.AppList
	if err := s.client.List(r.Context(), &allApps, &client.ListOptions{}); err != nil {
		writeError(w, err)
		return
	}
	for i := range allApps.Items {
		other := &allApps.Items[i]
		if other.Namespace == app.Namespace && other.Name == app.Name {
			continue
		}
		otherProject, _ := constants.ProjectFromControlNs(other.Namespace)
		for _, oe := range other.Spec.Environments {
			if oe.Domain == req.Domain || slices.Contains(oe.CustomDomains, req.Domain) {
				writeJSON(w, http.StatusConflict, errorResponse{
					fmt.Sprintf("domain %s is already used by %s in %s (%s)", req.Domain, other.Name, otherProject, oe.Name),
				})
				return
			}
		}
		for _, se := range other.Status.Environments {
			if se.Domain == req.Domain {
				writeJSON(w, http.StatusConflict, errorResponse{
					fmt.Sprintf("domain %s is already used by %s in %s (%s)", req.Domain, other.Name, otherProject, se.Name),
				})
				return
			}
		}
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
		Auto:    statusAutoDomain(app, envName),
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
		Auto:    statusAutoDomain(app, envName),
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

// statusAutoDomain returns the auto-generated domain from the app's status for
// the given environment, or empty if none exists.
func statusAutoDomain(app *mortisev1alpha1.App, envName string) string {
	for _, se := range app.Status.Environments {
		if se.Name == envName {
			return se.AutoDomain
		}
	}
	return ""
}

// domainValidateRequest is the JSON body for POST /api/domains/validate.
type domainValidateRequest struct {
	Domain         string `json:"domain"`
	ExcludeApp     string `json:"exclude_app,omitempty"`
	ExcludeProject string `json:"exclude_project,omitempty"`
}

// domainConflict describes which app/env already holds a domain.
type domainConflict struct {
	Project     string `json:"project"`
	App         string `json:"app"`
	Environment string `json:"environment"`
}

// domainValidateResponse is the result of a domain collision check.
type domainValidateResponse struct {
	Valid    bool            `json:"valid"`
	Conflict *domainConflict `json:"conflict,omitempty"`
}

// ValidateDomain checks whether a domain is already in use by any app.
//
// POST /api/domains/validate
//
// @Summary Validate a domain for conflicts
// @Description Checks if a domain is already assigned to any app across all projects.
// @Tags domains
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body domainValidateRequest true "Domain to validate"
// @Success 200 {object} domainValidateResponse
// @Failure 400 {object} errorResponse
// @Router /domains/validate [post]
func (s *Server) ValidateDomain(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, authz.Resource{Kind: "app"}, authz.ActionRead) {
		return
	}

	var req domainValidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if req.Domain == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"domain is required"})
		return
	}
	if !hostnameRegex.MatchString(req.Domain) {
		writeJSON(w, http.StatusBadRequest, errorResponse{"domain must be a valid hostname"})
		return
	}

	var apps mortisev1alpha1.AppList
	if err := s.client.List(r.Context(), &apps, &client.ListOptions{}); err != nil {
		writeError(w, err)
		return
	}

	for i := range apps.Items {
		app := &apps.Items[i]
		projectName, _ := constants.ProjectFromControlNs(app.Namespace)

		if req.ExcludeApp != "" && req.ExcludeProject != "" &&
			app.Name == req.ExcludeApp && projectName == req.ExcludeProject {
			continue
		}

		for _, env := range app.Spec.Environments {
			if env.Domain == req.Domain {
				writeJSON(w, http.StatusOK, domainValidateResponse{
					Valid: false,
					Conflict: &domainConflict{
						Project:     projectName,
						App:         app.Name,
						Environment: env.Name,
					},
				})
				return
			}
			if slices.Contains(env.CustomDomains, req.Domain) {
				writeJSON(w, http.StatusOK, domainValidateResponse{
					Valid: false,
					Conflict: &domainConflict{
						Project:     projectName,
						App:         app.Name,
						Environment: env.Name,
					},
				})
				return
			}
		}
		for _, se := range app.Status.Environments {
			if se.Domain == req.Domain {
				writeJSON(w, http.StatusOK, domainValidateResponse{
					Valid: false,
					Conflict: &domainConflict{
						Project:     projectName,
						App:         app.Name,
						Environment: se.Name,
					},
				})
				return
			}
		}
	}

	writeJSON(w, http.StatusOK, domainValidateResponse{Valid: true})
}
