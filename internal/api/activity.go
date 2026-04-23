package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/activity"
	"github.com/mortise-org/mortise/internal/auth"
	"github.com/mortise-org/mortise/internal/authz"
)

const (
	defaultActivityLimit = 100
	maxActivityLimit     = activity.Cap
)

// ListActivity returns recent project activity, newest first.
//
// GET /api/projects/{project}/activity?limit=100
func (s *Server) ListActivity(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "project", Project: projectName}, authz.ActionRead) {
		return
	}
	_, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}

	limit := defaultActivityLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{"limit must be a positive integer"})
			return
		}
		if parsed > maxActivityLimit {
			parsed = maxActivityLimit
		}
		limit = parsed
	}

	events, err := s.activityStore.List(r.Context(), projectName, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

// ListPlatformActivity returns recent activity across all projects the caller
// can read, newest first.
//
// GET /api/activity?limit=100
func (s *Server) ListPlatformActivity(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, authz.Resource{Kind: "project"}, authz.ActionRead) {
		return
	}

	limit := defaultActivityLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{"limit must be a positive integer"})
			return
		}
		if parsed > maxActivityLimit {
			parsed = maxActivityLimit
		}
		limit = parsed
	}

	projects, err := s.readableProjects(r)
	if err != nil {
		writeError(w, err)
		return
	}

	var merged []activity.Event
	for _, project := range projects {
		events, err := s.activityStore.List(r.Context(), project, limit)
		if err != nil {
			writeError(w, err)
			return
		}
		merged = append(merged, events...)
	}

	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Timestamp.After(merged[j].Timestamp)
	})
	if len(merged) > limit {
		merged = merged[:limit]
	}
	writeJSON(w, http.StatusOK, merged)
}

func (s *Server) readableProjects(r *http.Request) ([]string, error) {
	var projectList mortisev1alpha1.ProjectList
	if err := s.client.List(r.Context(), &projectList); err != nil {
		return nil, err
	}

	principal := PrincipalFromContext(r.Context())
	if principal != nil && (principal.Role == auth.RoleAdmin || principal.Role == auth.RoleViewer) {
		out := make([]string, 0, len(projectList.Items))
		for i := range projectList.Items {
			out = append(out, projectList.Items[i].Name)
		}
		return out, nil
	}

	var allMembers mortisev1alpha1.ProjectMemberList
	if err := s.client.List(r.Context(), &allMembers,
		client.MatchingLabels{"mortise.dev/member": "true"},
	); err != nil {
		return nil, err
	}

	memberProjects := make(map[string]bool, len(allMembers.Items))
	if principal != nil {
		for _, m := range allMembers.Items {
			if m.Spec.Email == principal.Email {
				memberProjects[m.Spec.Project] = true
			}
		}
	}

	out := make([]string, 0, len(projectList.Items))
	for i := range projectList.Items {
		projectName := projectList.Items[i].Name
		if !memberProjects[projectName] {
			continue
		}
		ok, err := s.authz.Authorize(r.Context(), *principal, authz.Resource{Kind: "project", Project: projectName}, authz.ActionRead)
		if err != nil {
			return nil, fmt.Errorf("authorize project %q: %w", projectName, err)
		}
		if ok {
			out = append(out, projectName)
		}
	}
	return out, nil
}

func (s *Server) recordActivity(r *http.Request, project, action, kind, resource, msg, actorOverride string) {
	if s.activityStore == nil || project == "" || action == "" {
		return
	}

	e := activity.Event{
		Timestamp:    time.Now().UTC(),
		Actor:        activityActor(r, actorOverride),
		Action:       action,
		ResourceKind: kind,
		ResourceName: resource,
		Project:      project,
		Message:      msg,
	}
	if err := s.activityStore.Append(r.Context(), e); err != nil {
		slog.Warn("Could not append activity event", "project", project, "action", action, "kind", kind, "resource", resource, "error", err)
	}
}

func activityActor(r *http.Request, actorOverride string) string {
	if actorOverride != "" {
		return actorOverride
	}
	if p := PrincipalFromContext(r.Context()); p != nil && p.Email != "" {
		return p.Email
	}
	return "system"
}
