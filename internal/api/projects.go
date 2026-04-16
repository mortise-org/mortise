package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/auth"
)

// ProjectNamespacePrefix is the name prefix of the Kubernetes namespace that
// backs each Project. A Project named "infra" runs in namespace
// "project-infra". Kept in sync with internal/controller.ProjectNamespacePrefix.
const ProjectNamespacePrefix = "project-"

// projectNamespace returns the backing namespace name for a Project.
func projectNamespace(projectName string) string {
	return ProjectNamespacePrefix + projectName
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
	if !requireAdmin(w, r) {
		return
	}

	var req createProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"name is required"})
		return
	}

	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{
			Name: req.Name,
		},
		Spec: mortisev1alpha1.ProjectSpec{
			Description: req.Description,
		},
	}

	if err := s.client.Create(r.Context(), project); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, toProjectResponse(project))
}

// ListProjects returns every Project in the cluster.
func (s *Server) ListProjects(w http.ResponseWriter, r *http.Request) {
	var list mortisev1alpha1.ProjectList
	if err := s.client.List(r.Context(), &list); err != nil {
		writeError(w, err)
		return
	}
	resp := make([]projectResponse, 0, len(list.Items))
	for i := range list.Items {
		resp = append(resp, toProjectResponse(&list.Items[i]))
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetProject returns a single Project.
func (s *Server) GetProject(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "project")

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
	if !requireAdmin(w, r) {
		return
	}

	name := chi.URLParam(r, "project")

	var project mortisev1alpha1.Project
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name}, &project); err != nil {
		writeError(w, err)
		return
	}

	if err := s.client.Delete(r.Context(), &project); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "terminating", "project": name})
}

// resolveProject is called at the top of every app/secret/log/deploy handler
// nested under /api/projects/{project}. It reads the {project} URL param,
// fetches the Project CRD, 404s if missing, and returns the backing namespace
// name the caller should use for its k8s operations.
//
// On any failure, resolveProject writes the HTTP response itself and returns
// ok=false; the caller should simply return.
func (s *Server) resolveProject(w http.ResponseWriter, r *http.Request) (namespace string, ok bool) {
	projectName := chi.URLParam(r, "project")
	if projectName == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"project is required"})
		return "", false
	}

	var project mortisev1alpha1.Project
	err := s.client.Get(r.Context(), types.NamespacedName{Name: projectName}, &project)
	if errors.IsNotFound(err) {
		writeJSON(w, http.StatusNotFound, errorResponse{fmt.Sprintf("project %q not found", projectName)})
		return "", false
	}
	if err != nil {
		writeError(w, err)
		return "", false
	}

	ns := project.Status.Namespace
	if ns == "" {
		ns = projectNamespace(project.Name)
	}
	return ns, true
}

// requireAdmin writes a 403 and returns false if the authenticated principal
// isn't an admin.
func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	p := PrincipalFromContext(r.Context())
	if p == nil || p.Role != auth.RoleAdmin {
		writeJSON(w, http.StatusForbidden, errorResponse{"admin role required"})
		return false
	}
	return true
}
