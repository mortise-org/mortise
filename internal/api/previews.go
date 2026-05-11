package api

import (
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
)

type previewSummaryResponse struct {
	Name   string `json:"name"`
	AppRef string `json:"appRef"`
	PR     struct {
		Number int    `json:"number"`
		Branch string `json:"branch"`
		SHA    string `json:"sha"`
	} `json:"pr"`
	Phase     string `json:"phase,omitempty"`
	URL       string `json:"url,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// ListPreviews returns preview environment summaries for a project.
//
// GET /api/projects/{project}/previews
//
// @Summary List preview environments
// @Description Returns active preview environments for a project
// @Tags previews
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Success 200 {array} previewSummaryResponse
// @Failure 401 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/previews [get]
func (s *Server) ListPreviews(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "project", Project: projectName}, authz.ActionRead) {
		return
	}

	var list mortisev1alpha1.PreviewEnvironmentList
	if err := s.client.List(r.Context(), &list, client.InNamespace(ns)); err != nil {
		writeError(w, r, err)
		return
	}

	resp := make([]previewSummaryResponse, 0, len(list.Items))
	for i := range list.Items {
		pe := &list.Items[i]
		if !pe.DeletionTimestamp.IsZero() {
			continue
		}
		var item previewSummaryResponse
		item.Name = pe.Name
		item.AppRef = pe.Spec.AppRef
		item.PR.Number = pe.Spec.PullRequest.Number
		item.PR.Branch = pe.Spec.PullRequest.Branch
		item.PR.SHA = pe.Spec.PullRequest.SHA
		item.Phase = string(pe.Status.Phase)
		item.URL = pe.Status.URL
		if pe.Status.ExpiresAt != nil {
			item.ExpiresAt = pe.Status.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		resp = append(resp, item)
	}

	writeJSON(w, http.StatusOK, resp)
}
