package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/auth"
)

// defaultProjectName is the name of the Project that is auto-created during
// first-user setup. "The workspace is never empty."
const defaultProjectName = "default"

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

	// Seed the `default` Project so the workspace is never empty. If it
	// already exists (e.g. re-run after a partial setup), that's fine.
	if err := s.ensureDefaultProject(r.Context()); err != nil {
		slog.Error("setup: failed to seed default project", "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{"failed to create default project: " + err.Error()})
		return
	}

	principal, err := s.auth.Authenticate(r.Context(), auth.Credentials{Email: req.Email, Password: req.Password})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
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

// ensureDefaultProject creates the `default` Project if it does not already
// exist. Idempotent; returns nil when the project is present (fresh or
// pre-existing).
func (s *Server) ensureDefaultProject(ctx context.Context) error {
	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: defaultProjectName},
		Spec: mortisev1alpha1.ProjectSpec{
			Description: "Default project created during first-user setup.",
		},
	}
	if err := s.client.Create(ctx, project); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	return nil
}
