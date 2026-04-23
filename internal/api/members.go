package api

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
)

// memberResponse is the JSON shape returned for project members.
type memberResponse struct {
	Email   string `json:"email"`
	Role    string `json:"role"`
	AddedAt string `json:"addedAt,omitempty"`
	AddedBy string `json:"addedBy,omitempty"`
}

// addMemberRequest is the JSON body for POST /api/projects/{project}/members.
type addMemberRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// updateMemberRequest is the JSON body for PATCH /api/projects/{project}/members/{email}.
type updateMemberRequest struct {
	Role string `json:"role"`
}

// memberCRDName returns the deterministic ProjectMember CRD name for an email.
func memberCRDName(email string) string {
	return "member-" + hex.EncodeToString([]byte(email))
}

// validProjectRole returns true if role is a valid ProjectRole value.
func validProjectRole(role string) bool {
	switch mortisev1alpha1.ProjectRole(role) {
	case mortisev1alpha1.ProjectRoleOwner, mortisev1alpha1.ProjectRoleDeveloper, mortisev1alpha1.ProjectRoleViewer:
		return true
	}
	return false
}

// ListMembers returns all members of a project.
func (s *Server) ListMembers(w http.ResponseWriter, r *http.Request) {
	_, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "project", Project: projectName}, authz.ActionRead) {
		return
	}

	ns := constants.ControlNamespace(projectName)
	var list mortisev1alpha1.ProjectMemberList
	if err := s.client.List(r.Context(), &list,
		client.InNamespace(ns),
		client.MatchingLabels{"mortise.dev/member": "true"},
	); err != nil {
		writeError(w, err)
		return
	}

	resp := make([]memberResponse, 0, len(list.Items))
	for _, m := range list.Items {
		resp = append(resp, memberResponse{
			Email:   m.Spec.Email,
			Role:    string(m.Spec.Role),
			AddedAt: m.Status.AddedAt,
			AddedBy: m.Status.AddedBy,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// AddMember adds a user as a member of a project.
func (s *Server) AddMember(w http.ResponseWriter, r *http.Request) {
	_, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "member", Project: projectName}, authz.ActionCreate) {
		return
	}

	var req addMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if req.Email == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"email is required"})
		return
	}
	if !validProjectRole(req.Role) {
		writeJSON(w, http.StatusBadRequest, errorResponse{"role must be owner, developer, or viewer"})
		return
	}

	// Validate that the user exists (check for user secret in mortise-system).
	userSecretName := "user-" + hex.EncodeToString([]byte(req.Email))
	var userSecret corev1.Secret
	if err := s.client.Get(r.Context(), types.NamespacedName{
		Name:      userSecretName,
		Namespace: "mortise-system",
	}, &userSecret); err != nil {
		if errors.IsNotFound(err) {
			writeJSON(w, http.StatusBadRequest, errorResponse{"user not found: " + req.Email})
			return
		}
		writeError(w, err)
		return
	}

	// Check if already a member.
	ns := constants.ControlNamespace(projectName)
	name := memberCRDName(req.Email)
	var existing mortisev1alpha1.ProjectMember
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &existing); err == nil {
		writeJSON(w, http.StatusConflict, errorResponse{"user is already a member of this project"})
		return
	}

	principal := PrincipalFromContext(r.Context())
	member := &mortisev1alpha1.ProjectMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"mortise.dev/project": projectName,
				"mortise.dev/member":  "true",
			},
		},
		Spec: mortisev1alpha1.ProjectMemberSpec{
			Email:   req.Email,
			Project: projectName,
			Role:    mortisev1alpha1.ProjectRole(req.Role),
		},
	}

	if err := s.client.Create(r.Context(), member); err != nil {
		writeError(w, err)
		return
	}

	// Set status fields via status subresource update.
	member.Status.AddedAt = time.Now().UTC().Format(time.RFC3339)
	if principal != nil {
		member.Status.AddedBy = principal.Email
	}
	if err := s.client.Status().Update(r.Context(), member); err != nil {
		// Non-fatal: the member was created, status is cosmetic.
		_ = err
	}

	s.recordActivity(r, projectName, "invite", "member", req.Email, "Added member "+req.Email+" as "+req.Role, "")

	writeJSON(w, http.StatusCreated, memberResponse{
		Email: req.Email,
		Role:  req.Role,
	})
}

// UpdateMember changes the role of an existing project member.
func (s *Server) UpdateMember(w http.ResponseWriter, r *http.Request) {
	_, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "member", Project: projectName}, authz.ActionUpdate) {
		return
	}

	email := chi.URLParam(r, "email")
	if email == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"email is required"})
		return
	}

	var req updateMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if !validProjectRole(req.Role) {
		writeJSON(w, http.StatusBadRequest, errorResponse{"role must be owner, developer, or viewer"})
		return
	}

	ns := constants.ControlNamespace(projectName)
	name := memberCRDName(email)
	var member mortisev1alpha1.ProjectMember
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &member); err != nil {
		writeError(w, err)
		return
	}

	member.Spec.Role = mortisev1alpha1.ProjectRole(req.Role)
	if err := s.client.Update(r.Context(), &member); err != nil {
		writeError(w, err)
		return
	}

	s.recordActivity(r, projectName, "update", "member", member.Spec.Email, "Updated member "+member.Spec.Email+" role to "+req.Role, "")

	writeJSON(w, http.StatusOK, memberResponse{
		Email:   member.Spec.Email,
		Role:    string(member.Spec.Role),
		AddedAt: member.Status.AddedAt,
		AddedBy: member.Status.AddedBy,
	})
}

// RemoveMember removes a member from a project.
func (s *Server) RemoveMember(w http.ResponseWriter, r *http.Request) {
	_, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "member", Project: projectName}, authz.ActionDelete) {
		return
	}

	email := chi.URLParam(r, "email")
	if email == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"email is required"})
		return
	}

	ns := constants.ControlNamespace(projectName)
	name := memberCRDName(email)

	var member mortisev1alpha1.ProjectMember
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &member); err != nil {
		writeError(w, err)
		return
	}

	// Prevent removing the last owner.
	if member.Spec.Role == mortisev1alpha1.ProjectRoleOwner {
		var allMembers mortisev1alpha1.ProjectMemberList
		if err := s.client.List(r.Context(), &allMembers,
			client.InNamespace(ns),
			client.MatchingLabels{"mortise.dev/member": "true"},
		); err != nil {
			writeError(w, err)
			return
		}
		ownerCount := 0
		for _, m := range allMembers.Items {
			if m.Spec.Role == mortisev1alpha1.ProjectRoleOwner {
				ownerCount++
			}
		}
		if ownerCount <= 1 {
			writeJSON(w, http.StatusBadRequest, errorResponse{"cannot remove the last owner of a project"})
			return
		}
	}

	if err := s.client.Delete(r.Context(), &member); err != nil {
		writeError(w, err)
		return
	}

	s.recordActivity(r, projectName, "remove", "member", email, "Removed member "+email, "")

	w.WriteHeader(http.StatusNoContent)
}
