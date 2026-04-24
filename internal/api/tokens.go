package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mortise-org/mortise/internal/authz"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	deployTokenPrefix        = "mrt_"
	projectDeployTokenPrefix = "mrt_pj_"
)

// createTokenRequest is the JSON body for POST /api/projects/{p}/apps/{a}/tokens.
type createTokenRequest struct {
	Environment string `json:"environment"`
	Name        string `json:"name"`
}

// tokenResponse is the JSON returned when creating a deploy token.
// The Token field is only populated on creation (never on list).
type tokenResponse struct {
	Token       string `json:"token,omitempty"`
	Name        string `json:"name"`
	Environment string `json:"environment,omitempty"`
	CreatedAt   string `json:"createdAt,omitempty"`
}

// createProjectTokenRequest is the JSON body for POST /api/projects/{p}/tokens.
type createProjectTokenRequest struct {
	Description string `json:"description"`
}

// CreateToken generates a deploy token, stores its hash as a k8s Secret, and
// returns the raw token value once.
//
// @Summary Create a deploy token for an app
// @Description Generates a deploy token scoped to an app and environment, stores its hash, and returns the raw token once
// @Tags tokens
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Param body body createTokenRequest true "Token name and environment"
// @Success 201 {object} tokenResponse
// @Failure 400 {object} errorResponse
// @Failure 409 {object} errorResponse
// @Router /projects/{project}/apps/{app}/tokens [post]
func (s *Server) CreateToken(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "token", Namespace: ns, Project: projectName}, authz.ActionCreate) {
		return
	}
	appName := chi.URLParam(r, "app")

	var req createTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"name is required"})
		return
	}
	if req.Environment == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"environment is required"})
		return
	}

	// Generate token: mrt_ + 32 random bytes hex-encoded.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"failed to generate token"})
		return
	}
	token := deployTokenPrefix + hex.EncodeToString(raw)

	// Store SHA-256 hash of the full token string.
	hash := sha256.Sum256([]byte(token))
	hashHex := hex.EncodeToString(hash[:])

	secretName := fmt.Sprintf("deploy-token-%s-%s", appName, req.Name)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: ns,
			Labels: map[string]string{
				"mortise.dev/deploy-token": "true",
				"mortise.dev/app":          appName,
				"mortise.dev/environment":  req.Environment,
				"mortise.dev/token-name":   req.Name,
			},
		},
		StringData: map[string]string{
			"token-hash": hashHex,
		},
	}

	if err := s.client.Create(r.Context(), secret); err != nil {
		writeError(w, err)
		return
	}

	s.recordActivity(r, projectName, "create", "token", req.Name, "Created deploy token "+req.Name+" for "+appName+" in "+req.Environment, "")

	writeJSON(w, http.StatusCreated, tokenResponse{
		Token:       token,
		Name:        req.Name,
		Environment: req.Environment,
	})
}

// ListTokens returns metadata for all deploy tokens scoped to an app.
//
// @Summary List deploy tokens for an app
// @Description Returns metadata (name, environment, creation time) for all deploy tokens scoped to an app
// @Tags tokens
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Success 200 {array} tokenResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app}/tokens [get]
func (s *Server) ListTokens(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "token", Namespace: ns, Project: projectName}, authz.ActionRead) {
		return
	}
	appName := chi.URLParam(r, "app")

	var list corev1.SecretList
	if err := s.client.List(r.Context(), &list,
		client.InNamespace(ns),
		client.MatchingLabels{
			"mortise.dev/deploy-token": "true",
			"mortise.dev/app":          appName,
		},
	); err != nil {
		writeError(w, err)
		return
	}

	resp := make([]tokenResponse, 0, len(list.Items))
	for i := range list.Items {
		sec := &list.Items[i]
		tr := tokenResponse{
			Name:        sec.Labels["mortise.dev/token-name"],
			Environment: sec.Labels["mortise.dev/environment"],
		}
		if !sec.CreationTimestamp.IsZero() {
			tr.CreatedAt = sec.CreationTimestamp.UTC().Format("2006-01-02T15:04:05Z")
		}
		resp = append(resp, tr)
	}

	writeJSON(w, http.StatusOK, resp)
}

// DeleteToken revokes a deploy token by deleting its backing Secret.
//
// @Summary Delete a deploy token
// @Description Revokes a deploy token by deleting its backing Secret
// @Tags tokens
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Param tokenName path string true "Token name"
// @Success 200 {object} map[string]string
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app}/tokens/{tokenName} [delete]
func (s *Server) DeleteToken(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "token", Namespace: ns, Project: projectName}, authz.ActionDelete) {
		return
	}
	appName := chi.URLParam(r, "app")
	tokenName := chi.URLParam(r, "tokenName")

	secretName := fmt.Sprintf("deploy-token-%s-%s", appName, tokenName)

	var secret corev1.Secret
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: secretName, Namespace: ns}, &secret); err != nil {
		writeError(w, err)
		return
	}

	if secret.Labels["mortise.dev/deploy-token"] != "true" {
		writeJSON(w, http.StatusNotFound, errorResponse{"token not found"})
		return
	}

	if err := s.client.Delete(r.Context(), &secret); err != nil {
		writeError(w, err)
		return
	}

	s.recordActivity(r, projectName, "delete", "token", tokenName, "Revoked deploy token "+tokenName+" for "+appName, "")

	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// validateDeployToken checks whether an mrt_ bearer token is valid for the
// given app and environment. Returns true if the token is valid.
func (s *Server) validateDeployToken(r *http.Request, ns, appName, env string) (bool, string) {
	header := r.Header.Get("Authorization")
	if header == "" || !strings.HasPrefix(header, "Bearer ") {
		return false, ""
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if !strings.HasPrefix(token, deployTokenPrefix) {
		return false, ""
	}

	hash := sha256.Sum256([]byte(token))
	hashHex := hex.EncodeToString(hash[:])

	// List all deploy token secrets for the app+env and check for a hash match.
	var list corev1.SecretList
	if err := s.client.List(r.Context(), &list,
		client.InNamespace(ns),
		client.MatchingLabels{
			"mortise.dev/deploy-token": "true",
			"mortise.dev/app":          appName,
			"mortise.dev/environment":  env,
		},
	); err != nil {
		return false, ""
	}

	for i := range list.Items {
		sec := &list.Items[i]
		stored := string(sec.Data["token-hash"])
		if stored == hashHex {
			name := sec.Labels["mortise.dev/token-name"]
			if name == "" {
				name = sec.Name
			}
			return true, name
		}
	}
	return false, ""
}

// validateProjectDeployToken checks whether an mrt_pj_ bearer token is valid
// for the given project. Project tokens grant deploy access to any app in
// the project, with no environment restriction.
func (s *Server) validateProjectDeployToken(r *http.Request, ns, projectName string) (bool, string) {
	header := r.Header.Get("Authorization")
	if header == "" || !strings.HasPrefix(header, "Bearer ") {
		return false, ""
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if !strings.HasPrefix(token, projectDeployTokenPrefix) {
		return false, ""
	}

	// Verify the embedded project name matches the target project.
	rest := strings.TrimPrefix(token, projectDeployTokenPrefix)
	idx := strings.LastIndex(rest, "_")
	if idx <= 0 {
		return false, ""
	}
	tokenProject := rest[:idx]
	if tokenProject != projectName {
		return false, ""
	}

	hash := sha256.Sum256([]byte(token))
	hashHex := hex.EncodeToString(hash[:])

	var list corev1.SecretList
	if err := s.client.List(r.Context(), &list,
		client.InNamespace(ns),
		client.MatchingLabels{
			"mortise.dev/deploy-token":  "true",
			"mortise.dev/project-token": "true",
		},
	); err != nil {
		return false, ""
	}

	for i := range list.Items {
		sec := &list.Items[i]
		stored := string(sec.Data["token_hash"])
		if stored == hashHex {
			return true, sec.Name
		}
	}
	return false, ""
}

// CreateProjectToken generates a project-scoped deploy token that grants
// deploy access to any app in the project.
//
// @Summary Create a project-scoped deploy token
// @Description Generates a deploy token scoped to an entire project, granting deploy access to any app
// @Tags tokens
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param body body createProjectTokenRequest true "Token description"
// @Success 201 {object} tokenResponse
// @Failure 400 {object} errorResponse
// @Router /projects/{project}/tokens [post]
func (s *Server) CreateProjectToken(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "token", Namespace: ns, Project: projectName}, authz.ActionCreate) {
		return
	}

	var req createProjectTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}

	// Generate token: mrt_pj_{project}_{32 random hex bytes}.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"failed to generate token"})
		return
	}
	token := projectDeployTokenPrefix + projectName + "_" + hex.EncodeToString(raw)

	hash := sha256.Sum256([]byte(token))
	hashHex := hex.EncodeToString(hash[:])

	// Short random suffix for the secret name.
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"failed to generate token"})
		return
	}

	secretName := fmt.Sprintf("deploy-token-pj-%s", hex.EncodeToString(suffix))
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: ns,
			Labels: map[string]string{
				"mortise.dev/deploy-token":  "true",
				"mortise.dev/project":       projectName,
				"mortise.dev/project-token": "true",
			},
		},
		StringData: map[string]string{
			"token_hash":  hashHex,
			"description": req.Description,
			"project":     projectName,
		},
	}

	if err := s.client.Create(r.Context(), secret); err != nil {
		writeError(w, err)
		return
	}

	s.recordActivity(r, projectName, "create", "token", secretName, "Created project deploy token "+secretName, "")

	writeJSON(w, http.StatusCreated, tokenResponse{
		Token:     token,
		Name:      secretName,
		CreatedAt: secret.CreationTimestamp.UTC().Format("2006-01-02T15:04:05Z"),
	})
}

// ListProjectTokens returns metadata for all project-scoped deploy tokens.
//
// @Summary List project-scoped deploy tokens
// @Description Returns metadata for all project-scoped deploy tokens
// @Tags tokens
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Success 200 {array} tokenResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/tokens [get]
func (s *Server) ListProjectTokens(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "token", Namespace: ns, Project: projectName}, authz.ActionRead) {
		return
	}

	var list corev1.SecretList
	if err := s.client.List(r.Context(), &list,
		client.InNamespace(ns),
		client.MatchingLabels{
			"mortise.dev/deploy-token":  "true",
			"mortise.dev/project-token": "true",
		},
	); err != nil {
		writeError(w, err)
		return
	}

	type projectTokenResponse struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		CreatedAt   string `json:"createdAt,omitempty"`
	}

	resp := make([]projectTokenResponse, 0, len(list.Items))
	for i := range list.Items {
		sec := &list.Items[i]
		tr := projectTokenResponse{
			Name:        sec.Name,
			Description: string(sec.Data["description"]),
		}
		if !sec.CreationTimestamp.IsZero() {
			tr.CreatedAt = sec.CreationTimestamp.UTC().Format("2006-01-02T15:04:05Z")
		}
		resp = append(resp, tr)
	}

	writeJSON(w, http.StatusOK, resp)
}

// DeleteProjectToken revokes a project-scoped deploy token by deleting its
// backing Secret.
//
// @Summary Delete a project-scoped deploy token
// @Description Revokes a project-scoped deploy token by deleting its backing Secret
// @Tags tokens
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param tokenName path string true "Token name"
// @Success 204
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/tokens/{tokenName} [delete]
func (s *Server) DeleteProjectToken(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "token", Namespace: ns, Project: projectName}, authz.ActionDelete) {
		return
	}

	tokenName := chi.URLParam(r, "tokenName")

	var secret corev1.Secret
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: tokenName, Namespace: ns}, &secret); err != nil {
		writeError(w, err)
		return
	}

	if secret.Labels["mortise.dev/project-token"] != "true" {
		writeJSON(w, http.StatusNotFound, errorResponse{"project token not found"})
		return
	}

	if err := s.client.Delete(r.Context(), &secret); err != nil {
		writeError(w, err)
		return
	}

	s.recordActivity(r, projectName, "delete", "token", tokenName, "Revoked project deploy token "+tokenName, "")

	w.WriteHeader(http.StatusNoContent)
}
