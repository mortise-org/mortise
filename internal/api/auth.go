package api

import (
	"encoding/json"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mortise-org/mortise/internal/auth"
)

type setupRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type authResponse struct {
	Token string         `json:"token"`
	User  auth.Principal `json:"user"`
}

type statusResponse struct {
	SetupRequired bool `json:"setupRequired"`
}

// @Summary Check setup status
// @Description Reports whether first-user setup is required (no users exist yet)
// @Tags auth
// @Produce json
// @Success 200 {object} statusResponse
// @Failure 500 {object} errorResponse
// @Router /auth/status [get]
//
// Status reports whether first-user setup is required (no users exist yet).
// Unauthenticated so the UI can check before the user signs in.
func (s *Server) Status(w http.ResponseWriter, r *http.Request) {
	users, err := s.auth.ListUsers(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{SetupRequired: len(users) == 0})
}

// @Summary First-time setup
// @Description Creates the first admin user. Returns 409 if any user already exists.
// @Tags auth
// @Accept json
// @Produce json
// @Param body body setupRequest true "Admin credentials"
// @Success 201 {object} authResponse
// @Failure 400 {object} errorResponse
// @Failure 409 {object} errorResponse
// @Failure 500 {object} errorResponse
// @Failure 501 {object} errorResponse
// @Router /auth/setup [post]
//
// Setup creates the first admin user and the `default` Project. Returns 409 if
// any user already exists.
func (s *Server) Setup(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON"})
		return
	}
	if req.Email == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"email and password required"})
		return
	}
	if len(req.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, errorResponse{"password must be at least 8 characters"})
		return
	}

	native, ok := s.auth.(*auth.NativeAuthProvider)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, errorResponse{"setup requires native auth provider"})
		return
	}

	// Atomic setup claim: create a sentinel ConfigMap. If it already exists,
	// another request won the race and setup is complete.
	sentinel := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mortise-setup-complete",
			Namespace: "mortise-system",
		},
	}
	if err := s.client.Create(r.Context(), sentinel); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			writeJSON(w, http.StatusConflict, errorResponse{"setup already complete"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}

	if err := native.CreateUser(r.Context(), req.Email, req.Password, auth.RoleAdmin); err != nil {
		_ = s.client.Delete(r.Context(), sentinel)
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}

	// No default project seeded — users create their first project explicitly.

	// Skip re-authentication — we just created the user, no need to read it
	// back from the cache (which may not have synced yet).
	principal := auth.Principal{
		ID:    req.Email,
		Email: req.Email,
		Role:  auth.RoleAdmin,
	}

	token, err := s.jwt.GenerateToken(r.Context(), principal)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, authResponse{Token: token, User: principal})
}

// @Summary Refresh JWT token
// @Description Issues a new JWT from an existing token (valid or expired within 7 days)
// @Tags auth
// @Produce json
// @Security BearerAuth
// @Success 200 {object} authResponse
// @Failure 401 {object} errorResponse
// @Failure 500 {object} errorResponse
// @Router /auth/refresh [post]
//
// Refresh issues a new JWT from a valid or recently-expired token. The token
// may be expired for up to 7 days and still be eligible for refresh.
func (s *Server) Refresh(w http.ResponseWriter, r *http.Request) {
	header := r.Header.Get("Authorization")
	if header == "" || !strings.HasPrefix(header, "Bearer ") {
		writeJSON(w, http.StatusUnauthorized, errorResponse{"missing or invalid Authorization header"})
		return
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if token == "" {
		writeJSON(w, http.StatusUnauthorized, errorResponse{"empty bearer token"})
		return
	}

	principal, tokenGen, err := s.jwt.ValidateTokenForRefresh(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{"token not eligible for refresh"})
		return
	}

	native, ok := s.auth.(*auth.NativeAuthProvider)
	if ok {
		if err := native.CheckPasswordGen(r.Context(), principal.Email, tokenGen); err != nil {
			writeJSON(w, http.StatusUnauthorized, errorResponse{"session invalidated by password change"})
			return
		}
	}

	newToken, err := s.jwt.GenerateToken(r.Context(), principal)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, authResponse{Token: newToken, User: principal})
}

// @Summary Log in
// @Description Authenticates a user and returns a JWT
// @Tags auth
// @Accept json
// @Produce json
// @Param body body loginRequest true "User credentials"
// @Success 200 {object} authResponse
// @Failure 400 {object} errorResponse
// @Failure 401 {object} errorResponse
// @Failure 500 {object} errorResponse
// @Router /auth/login [post]
//
// Login authenticates a user and returns a JWT.
func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON"})
		return
	}

	principal, err := s.auth.Authenticate(r.Context(), auth.Credentials{Email: req.Email, Password: req.Password})
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{"invalid credentials"})
		return
	}

	token, err := s.jwt.GenerateToken(r.Context(), principal)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, authResponse{Token: token, User: principal})
}
