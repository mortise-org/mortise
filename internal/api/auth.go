package api

import (
	"encoding/json"
	"net/http"

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

	users, err := s.auth.ListUsers(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}
	if len(users) > 0 {
		writeJSON(w, http.StatusConflict, errorResponse{"setup already complete"})
		return
	}

	native, ok := s.auth.(*auth.NativeAuthProvider)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, errorResponse{"setup requires native auth provider"})
		return
	}
	if err := native.CreateUser(r.Context(), req.Email, req.Password, auth.RoleAdmin); err != nil {
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

