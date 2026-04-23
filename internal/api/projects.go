package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/auth"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
)

// maxProjectNameLen caps the Project name so env namespaces (`pj-{name}-{env}`)
// have reasonable room for env-name characters. With a 63-char namespace limit
// and `pj-` (3) + "-" (1) = 4 chars of overhead plus a worst-case env name,
// we cap the project at 30 chars — leaves 29 chars for env names, which
// comfortably covers `production`, `staging`, `preview`, etc. The controller
// rejects overflow at admission time too.
const maxProjectNameLen = 30

// dns1123LabelRegex matches a valid DNS-1123 label: lowercase alphanumerics
// and hyphens, must start/end with an alphanumeric. Project names must be
// DNS labels (not subdomains) because they're interpolated into a namespace.
var dns1123LabelRegex = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// validateProjectName returns an error message describing why name is invalid,
// or "" if it's acceptable.
func validateProjectName(name string) string {
	return validateDNSLabel("name", name, maxProjectNameLen)
}

// projectNamespace returns the control namespace name for a Project.
func projectNamespace(projectName string) string {
	return constants.ControlNamespace(projectName)
}

// createProjectRequest is the JSON body for creating a Project.
type createProjectRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// projectResponse is the JSON shape returned for Project GETs. It is a flat,
// stable subset of the CRD — callers shouldn't need to understand Kubernetes
// metadata layout to read back a project.
type projectResponse struct {
	Name        string                       `json:"name"`
	Description string                       `json:"description,omitempty"`
	Namespace   string                       `json:"namespace"`
	Phase       mortisev1alpha1.ProjectPhase `json:"phase,omitempty"`
	AppCount    int32                        `json:"appCount"`
	CreatedAt   string                       `json:"createdAt,omitempty"`
}

func toProjectResponse(p *mortisev1alpha1.Project) projectResponse {
	ns := p.Status.Namespace
	if ns == "" {
		ns = projectNamespace(p.Name)
	}
	resp := projectResponse{
		Name:        p.Name,
		Description: p.Spec.Description,
		Namespace:   ns,
		Phase:       p.Status.Phase,
		AppCount:    p.Status.AppCount,
	}
	if !p.CreationTimestamp.IsZero() {
		resp.CreatedAt = p.CreationTimestamp.UTC().Format("2006-01-02T15:04:05Z")
	}
	return resp
}

// CreateProject creates a new Project. Admin-only.
func (s *Server) CreateProject(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, authz.Resource{Kind: "project"}, authz.ActionCreate) {
		return
	}

	var req createProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if msg := validateProjectName(req.Name); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{msg})
		return
	}

	// Stamp the creator so the Project controller can auto-create an owner
	// ProjectMember once the control namespace is ready.
	annotations := map[string]string{}
	principal := PrincipalFromContext(r.Context())
	if principal != nil {
		annotations["mortise.dev/created-by"] = principal.Email
	}

	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{
			Name:        req.Name,
			Annotations: annotations,
		},
		Spec: mortisev1alpha1.ProjectSpec{
			Description: req.Description,
		},
	}

	if err := s.client.Create(r.Context(), project); err != nil {
		writeError(w, err)
		return
	}

	s.recordActivity(r, project.Name, "create", "project", project.Name, "Created project "+project.Name, "")

	writeJSON(w, http.StatusCreated, toProjectResponse(project))
}

// ListProjects returns Projects the caller has access to. Admins and platform
// viewers see all projects; regular members see only projects where they hold
// a ProjectMember.
func (s *Server) ListProjects(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, authz.Resource{Kind: "project"}, authz.ActionRead) {
		return
	}
	var list mortisev1alpha1.ProjectList
	if err := s.client.List(r.Context(), &list); err != nil {
		writeError(w, err)
		return
	}

	principal := PrincipalFromContext(r.Context())

	// Admins and platform viewers see everything.
	if principal != nil && (principal.Role == auth.RoleAdmin || principal.Role == auth.RoleViewer) {
		resp := make([]projectResponse, 0, len(list.Items))
		for i := range list.Items {
			resp = append(resp, toProjectResponse(&list.Items[i]))
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// For regular members, build a set of projects where this user has membership.
	var allMembers mortisev1alpha1.ProjectMemberList
	if err := s.client.List(r.Context(), &allMembers,
		client.MatchingLabels{"mortise.dev/member": "true"},
	); err != nil {
		writeError(w, err)
		return
	}
	memberProjects := make(map[string]bool, len(allMembers.Items))
	if principal != nil {
		for _, m := range allMembers.Items {
			if m.Spec.Email == principal.Email {
				memberProjects[m.Spec.Project] = true
			}
		}
	}

	resp := make([]projectResponse, 0, len(list.Items))
	for i := range list.Items {
		if memberProjects[list.Items[i].Name] {
			resp = append(resp, toProjectResponse(&list.Items[i]))
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetProject returns a single Project.
func (s *Server) GetProject(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "project", Project: projectName}, authz.ActionRead) {
		return
	}
	name := projectName

	var project mortisev1alpha1.Project
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name}, &project); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProjectResponse(&project))
}

// DeleteProject deletes a Project. The controller's finalizer handles
// tearing down the backing namespace, which cascades to every App inside.
// Admin-only.
func (s *Server) DeleteProject(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "project", Project: projectName}, authz.ActionDelete) {
		return
	}

	name := projectName

	var project mortisev1alpha1.Project
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name}, &project); err != nil {
		writeError(w, err)
		return
	}

	if err := s.client.Delete(r.Context(), &project); err != nil {
		writeError(w, err)
		return
	}

	s.recordActivity(r, name, "delete", "project", name, "Deleted project "+name, "")

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "terminating", "project": name})
}

// resolveProject is called at the top of every app/secret/log/deploy handler
// nested under /api/projects/{project}. It reads the {project} URL param,
// fetches the Project CRD, 404s if missing, and returns the control namespace
// name and the project name. The control namespace holds CRDs (App,
// PreviewEnvironment) and App-owned shared resources (build logs, deploy
// tokens, registry pull secrets). Per-env workload namespaces are derived
// from projectName via constants.EnvNamespace.
//
// On any failure, resolveProject writes the HTTP response itself and returns
// ok=false; the caller should simply return.
func (s *Server) resolveProject(w http.ResponseWriter, r *http.Request) (namespace, projectName string, ok bool) {
	projectName = chi.URLParam(r, "project")
	if projectName == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"project is required"})
		return "", "", false
	}

	var project mortisev1alpha1.Project
	err := s.client.Get(r.Context(), types.NamespacedName{Name: projectName}, &project)
	if errors.IsNotFound(err) {
		writeJSON(w, http.StatusNotFound, errorResponse{fmt.Sprintf("project %q not found", projectName)})
		return "", "", false
	}
	if err != nil {
		writeError(w, err)
		return "", "", false
	}

	ns := project.Status.Namespace
	if ns == "" {
		ns = projectNamespace(project.Name)
	}
	return ns, project.Name, true
}
