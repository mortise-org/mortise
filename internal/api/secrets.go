package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
)

// createSecretRequest is the JSON body for upserting a secret.
type createSecretRequest struct {
	Name string            `json:"name"`
	Data map[string]string `json:"data"`
}

// secretResponse is the JSON response for a secret (values redacted).
type secretResponse struct {
	Name string   `json:"name"`
	Keys []string `json:"keys"`
}

// queryEnv returns the environment query parameter, checking "env" first and
// falling back to "environment" for backwards compatibility. Returns "" if
// neither is set.
func queryEnv(r *http.Request) string {
	if v := r.URL.Query().Get("env"); v != "" {
		return v
	}
	return r.URL.Query().Get("environment")
}

// envFromQuery returns the environment query parameter, defaulting to
// "production" when absent. User-facing Secrets are scoped to a specific env
// namespace because workload pods can only mount Secrets from their own
// namespace.
func envFromQuery(r *http.Request) string {
	if env := queryEnv(r); env != "" {
		return env
	}
	return "production"
}

// @Summary Create a secret for an app
// @Description Creates a new Kubernetes Secret scoped to an app and environment
// @Tags secrets
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Param environment query string false "Environment name (defaults to production)"
// @Param body body createSecretRequest true "Secret name and data"
// @Success 201 {object} secretResponse
// @Failure 400 {object} errorResponse
// @Failure 409 {object} errorResponse
// @Router /projects/{project}/apps/{app}/secrets [post]
func (s *Server) CreateSecret(w http.ResponseWriter, r *http.Request) {
	_, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "secret", Project: projectName}, authz.ActionCreate) {
		return
	}
	appName := chi.URLParam(r, "app")
	envName := envFromQuery(r)
	envNs := constants.EnvNamespace(projectName, envName)

	var req createSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"name is required"})
		return
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: envNs,
			Labels: map[string]string{
				constants.AppNameLabel:         appName,
				"app.kubernetes.io/managed-by": "mortise",
			},
		},
		StringData: req.Data,
	}

	if err := s.client.Create(r.Context(), secret); err != nil {
		writeError(w, err)
		return
	}

	s.recordActivity(r, projectName, "create", "secret", req.Name, "Created secret "+req.Name+" for "+appName+" in "+envName, "")

	writeJSON(w, http.StatusCreated, toSecretResponse(secret))
}

// @Summary List secrets for an app
// @Description Returns metadata (name and keys, no values) for all Mortise-managed secrets scoped to an app
// @Tags secrets
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Param environment query string false "Environment name (defaults to production)"
// @Success 200 {array} secretResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app}/secrets [get]
func (s *Server) ListSecrets(w http.ResponseWriter, r *http.Request) {
	_, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "secret", Project: projectName}, authz.ActionRead) {
		return
	}
	appName := chi.URLParam(r, "app")
	envNs := constants.EnvNamespace(projectName, envFromQuery(r))

	var list corev1.SecretList
	if err := s.client.List(r.Context(), &list,
		client.InNamespace(envNs),
		client.MatchingLabels{
			constants.AppNameLabel:         appName,
			"app.kubernetes.io/managed-by": "mortise",
		},
	); err != nil {
		writeError(w, err)
		return
	}

	resp := make([]secretResponse, 0, len(list.Items))
	for i := range list.Items {
		resp = append(resp, toSecretResponse(&list.Items[i]))
	}

	writeJSON(w, http.StatusOK, resp)
}

// @Summary Delete a secret
// @Description Deletes a Mortise-managed secret by name for a given app and environment
// @Tags secrets
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Param secretName path string true "Secret name"
// @Param environment query string false "Environment name (defaults to production)"
// @Success 200 {object} map[string]string
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app}/secrets/{secretName} [delete]
func (s *Server) DeleteSecret(w http.ResponseWriter, r *http.Request) {
	_, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "secret", Project: projectName}, authz.ActionDelete) {
		return
	}
	appName := chi.URLParam(r, "app")
	secretName := chi.URLParam(r, "secretName")
	envName := envFromQuery(r)
	envNs := constants.EnvNamespace(projectName, envName)

	var secret corev1.Secret
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: secretName, Namespace: envNs}, &secret); err != nil {
		writeError(w, err)
		return
	}

	// Only delete secrets managed by mortise.
	if secret.Labels["app.kubernetes.io/managed-by"] != "mortise" {
		writeJSON(w, http.StatusForbidden, errorResponse{"secret is not managed by mortise"})
		return
	}

	// Verify the secret belongs to the app from the URL.
	if secret.Labels[constants.AppNameLabel] != appName {
		writeJSON(w, http.StatusNotFound, errorResponse{"secret not found for this app"})
		return
	}

	if err := s.client.Delete(r.Context(), &secret); err != nil {
		if errors.IsNotFound(err) {
			writeJSON(w, http.StatusNotFound, errorResponse{err.Error()})
			return
		}
		writeError(w, err)
		return
	}

	s.recordActivity(r, projectName, "delete", "secret", secretName, "Deleted secret "+secretName+" for "+appName+" in "+envName, "")

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func toSecretResponse(s *corev1.Secret) secretResponse {
	keys := make([]string, 0, len(s.Data)+len(s.StringData))
	for k := range s.Data {
		keys = append(keys, k)
	}
	for k := range s.StringData {
		keys = append(keys, k)
	}
	return secretResponse{Name: s.Name, Keys: keys}
}
