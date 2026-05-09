package api

import (
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mortise-org/mortise/internal/auth"
	"github.com/mortise-org/mortise/internal/authz"
)

// userResponse is the JSON shape returned for admin user endpoints.
type userResponse struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

// createUserRequest is the JSON body for POST /api/admin/users.
type createUserRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// updateUserRoleRequest is the JSON body for PATCH /api/admin/users/{email}.
type updateUserRoleRequest struct {
	Role string `json:"role"`
}

// validPlatformRole returns true if role is a valid platform Role value.
func validPlatformRole(role string) bool {
	switch auth.Role(role) {
	case auth.RoleAdmin, auth.RoleMember, auth.RoleViewer:
		return true
	}
	return false
}

// @Summary List users
// @Description Returns all platform users. Admin-only.
// @Tags users
// @Produce json
// @Security BearerAuth
// @Success 200 {array} userResponse
// @Failure 401 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Failure 500 {object} errorResponse
// @Router /admin/users [get]
//
// ListUsers returns all platform users. Admin-only.
func (s *Server) ListUsers(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, authz.Resource{Kind: "user"}, authz.ActionRead) {
		return
	}

	users, err := s.auth.ListUsers(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}

	resp := make([]userResponse, 0, len(users))
	for _, u := range users {
		resp = append(resp, userResponse{
			ID:    u.ID,
			Email: u.Email,
			Role:  string(u.Role),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// @Summary Create a user
// @Description Creates a new platform user. Admin-only.
// @Tags users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body createUserRequest true "User details"
// @Success 201 {object} userResponse
// @Failure 400 {object} errorResponse
// @Failure 401 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Failure 500 {object} errorResponse
// @Failure 501 {object} errorResponse
// @Router /admin/users [post]
//
// CreateUser creates a new platform user. Admin-only.
func (s *Server) CreateUser(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, authz.Resource{Kind: "user"}, authz.ActionCreate) {
		return
	}

	var req createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if req.Email == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"email and password are required"})
		return
	}
	if !validPlatformRole(req.Role) {
		writeJSON(w, http.StatusBadRequest, errorResponse{"role must be admin, member, or viewer"})
		return
	}

	native, ok := s.auth.(*auth.NativeAuthProvider)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, errorResponse{"user management requires native auth provider"})
		return
	}

	if err := native.CreateUser(r.Context(), req.Email, req.Password, auth.Role(req.Role)); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, userResponse{
		ID:    req.Email,
		Email: req.Email,
		Role:  req.Role,
	})
}

// @Summary Update a user's role
// @Description Changes a platform user's role. Admin-only.
// @Tags users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param email path string true "User email"
// @Param body body updateUserRoleRequest true "New role"
// @Success 200 {object} userResponse
// @Failure 400 {object} errorResponse
// @Failure 401 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /admin/users/{email} [patch]
//
// UpdateUserRole changes a platform user's role. Admin-only.
func (s *Server) UpdateUserRole(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, authz.Resource{Kind: "user"}, authz.ActionUpdate) {
		return
	}

	email := chi.URLParam(r, "email")
	if email == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"email is required"})
		return
	}

	var req updateUserRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if !validPlatformRole(req.Role) {
		writeJSON(w, http.StatusBadRequest, errorResponse{"role must be admin, member, or viewer"})
		return
	}

	// Read the user secret, update the role field, and save.
	secretName := "user-" + hex.EncodeToString([]byte(email))
	var secret corev1.Secret
	if err := s.client.Get(r.Context(), types.NamespacedName{
		Name:      secretName,
		Namespace: "mortise-system",
	}, &secret); err != nil {
		writeError(w, r, err)
		return
	}

	secret.Data["role"] = []byte(req.Role)
	if err := s.client.Update(r.Context(), &secret); err != nil {
		writeError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, userResponse{
		ID:    email,
		Email: email,
		Role:  req.Role,
	})
}

// @Summary Delete a user
// @Description Removes a platform user. Admin-only.
// @Tags users
// @Security BearerAuth
// @Param email path string true "User email"
// @Success 204
// @Failure 400 {object} errorResponse
// @Failure 401 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Failure 500 {object} errorResponse
// @Router /admin/users/{email} [delete]
//
// DeleteUser removes a platform user. Admin-only.
func (s *Server) DeleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, authz.Resource{Kind: "user"}, authz.ActionDelete) {
		return
	}

	email := chi.URLParam(r, "email")
	if email == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"email is required"})
		return
	}

	if err := s.auth.RevokeUser(r.Context(), email); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// resetPasswordRequest is the JSON body for POST /api/admin/users/{email}/password.
type resetPasswordRequest struct {
	Password string `json:"password"`
}

// @Summary Reset a user's password
// @Description Sets a new password for a user. Password must be provided by the client. Admin-only.
// @Tags users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param email path string true "User email"
// @Param body body resetPasswordRequest true "New password"
// @Success 200 {object} map[string]string
// @Failure 400 {object} errorResponse
// @Failure 401 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Failure 501 {object} errorResponse
// @Router /admin/users/{email}/password [post]
//
// ResetUserPassword sets a new password for a user. Admin-only.
func (s *Server) ResetUserPassword(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, authz.Resource{Kind: "user"}, authz.ActionUpdate) {
		return
	}

	email := chi.URLParam(r, "email")
	if email == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"email is required"})
		return
	}

	native, ok := s.auth.(*auth.NativeAuthProvider)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, errorResponse{"password management requires native auth provider"})
		return
	}

	var req resetPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if req.Password == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"password is required"})
		return
	}

	if err := native.UpdatePassword(r.Context(), email, req.Password); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// changePasswordRequest is the JSON body for POST /api/me/password.
type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// @Summary Change own password
// @Description Allows an authenticated user to change their own password. Requires current password verification.
// @Tags auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body changePasswordRequest true "Current and new password"
// @Success 200 {object} map[string]string
// @Failure 400 {object} errorResponse
// @Failure 401 {object} errorResponse
// @Failure 501 {object} errorResponse
// @Router /me/password [post]
//
// ChangePassword allows authenticated users to change their own password.
func (s *Server) ChangePassword(w http.ResponseWriter, r *http.Request) {
	principal := PrincipalFromContext(r.Context())
	if principal == nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{"not authenticated"})
		return
	}

	native, ok := s.auth.(*auth.NativeAuthProvider)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, errorResponse{"password management requires native auth provider"})
		return
	}

	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if req.CurrentPassword == "" || req.NewPassword == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"current_password and new_password are required"})
		return
	}

	if err := native.VerifyPassword(r.Context(), principal.Email, req.CurrentPassword); err != nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{"current password is incorrect"})
		return
	}

	if err := native.UpdatePassword(r.Context(), principal.Email, req.NewPassword); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
