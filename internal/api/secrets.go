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

func (s *Server) CreateSecret(w http.ResponseWriter, r *http.Request) {
	_, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "secret", Project: projectName}, authz.ActionCreate) {
		return
	}
	appName := chi.URLParam(r, "app")
	envNs := constants.EnvNamespace(projectName, envFromQuery(r))

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

	writeJSON(w, http.StatusCreated, toSecretResponse(secret))
}

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
	envNs := constants.EnvNamespace(projectName, envFromQuery(r))

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
