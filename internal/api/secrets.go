package api

import (
	"encoding/json"
	"fmt"
	"net/http"

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

type secretTarget struct {
	project *mortisev1alpha1.Project
	app     *mortisev1alpha1.App
	envName string
	envNs   string
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
// @Failure 404 {object} errorResponse
// @Failure 409 {object} errorResponse
// @Router /projects/{project}/apps/{app}/secrets [post]
func (s *Server) CreateSecret(w http.ResponseWriter, r *http.Request) {
	target, ok := s.resolveSecretTarget(w, r)
	if !ok {
		return
	}
	projectName := target.project.Name
	if !s.authorize(w, r, authz.Resource{Kind: "secret", Project: projectName, Environment: envFromQuery(r)}, authz.ActionCreate) {
		return
	}

	var req createSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if msg := validateDNSLabel("name", req.Name, 253); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{msg})
		return
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: target.envNs,
			Labels: map[string]string{
				constants.AppNameLabel:         target.app.Name,
				constants.ProjectLabel:         projectName,
				"app.kubernetes.io/managed-by": "mortise",
			},
		},
		StringData: req.Data,
	}

	if err := s.client.Create(r.Context(), secret); err != nil {
		writeError(w, r, err)
		return
	}

	s.recordActivity(r, projectName, "create", "secret", req.Name, "Created secret "+req.Name+" for "+target.app.Name+" in "+target.envName, "")

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
// @Failure 400 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app}/secrets [get]
func (s *Server) ListSecrets(w http.ResponseWriter, r *http.Request) {
	target, ok := s.resolveSecretTarget(w, r)
	if !ok {
		return
	}
	projectName := target.project.Name
	if !s.authorize(w, r, authz.Resource{Kind: "secret", Project: projectName}, authz.ActionRead) {
		return
	}

	var list corev1.SecretList
	if err := s.client.List(r.Context(), &list,
		client.InNamespace(target.envNs),
		client.MatchingLabels{
			constants.AppNameLabel:         target.app.Name,
			"app.kubernetes.io/managed-by": "mortise",
		},
	); err != nil {
		writeError(w, r, err)
		return
	}

	resp := make([]secretResponse, 0, len(list.Items))
	for i := range list.Items {
		if isInternalAppEnvSecret(target.app.Name, &list.Items[i]) {
			continue
		}
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
// @Failure 400 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app}/secrets/{secretName} [delete]
func (s *Server) DeleteSecret(w http.ResponseWriter, r *http.Request) {
	target, ok := s.resolveSecretTarget(w, r)
	if !ok {
		return
	}
	projectName := target.project.Name
	if !s.authorize(w, r, authz.Resource{Kind: "secret", Project: projectName, Environment: envFromQuery(r)}, authz.ActionDelete) {
		return
	}
	secretName := chi.URLParam(r, "secretName")

	var secret corev1.Secret
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: secretName, Namespace: target.envNs}, &secret); err != nil {
		writeError(w, r, err)
		return
	}

	// Only delete secrets managed by mortise.
	if secret.Labels["app.kubernetes.io/managed-by"] != "mortise" {
		writeJSON(w, http.StatusForbidden, errorResponse{"secret is not managed by mortise"})
		return
	}

	// Verify the secret belongs to the app from the URL.
	if secret.Labels[constants.AppNameLabel] != target.app.Name {
		writeJSON(w, http.StatusNotFound, errorResponse{"secret not found for this app"})
		return
	}
	if isInternalAppEnvSecret(target.app.Name, &secret) {
		writeJSON(w, http.StatusForbidden, errorResponse{"secret is reserved for mortise runtime state"})
		return
	}

	if err := s.client.Delete(r.Context(), &secret); err != nil {
		if errors.IsNotFound(err) {
			writeJSON(w, http.StatusNotFound, errorResponse{err.Error()})
			return
		}
		writeError(w, r, err)
		return
	}

	s.recordActivity(r, projectName, "delete", "secret", secretName, "Deleted secret "+secretName+" for "+target.app.Name+" in "+target.envName, "")

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) resolveSecretTarget(w http.ResponseWriter, r *http.Request) (*secretTarget, bool) {
	project, ok := s.getProject(w, r)
	if !ok {
		return nil, false
	}
	appName := chi.URLParam(r, "app")
	envName := envFromQuery(r)
	if indexOfEnv(project, envName) < 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{fmt.Sprintf(
			"environment %q is not declared on project %q — add it via POST /api/projects/%s/environments first",
			envName, project.Name, project.Name)})
		return nil, false
	}

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: projectNs(project)}, &app); err != nil {
		writeError(w, r, err)
		return nil, false
	}
	if !appParticipatesInEnv(&app, envName) {
		writeJSON(w, http.StatusNotFound, errorResponse{fmt.Sprintf("environment %q is disabled for app %q", envName, app.Name)})
		return nil, false
	}
	envNs, err := envNamespace(&app, envName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return nil, false
	}

	return &secretTarget{
		project: project,
		app:     &app,
		envName: envName,
		envNs:   envNs,
	}, true
}

func isInternalAppEnvSecret(appName string, secret *corev1.Secret) bool {
	return secret.Name == appName+"-env"
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
