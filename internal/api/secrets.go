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
)

// createSecretRequest is the JSON body for creating a secret.
type createSecretRequest struct {
	Name string            `json:"name"`
	Data map[string]string `json:"data"`
}

// secretResponse is the JSON response for a secret (values redacted).
type secretResponse struct {
	Name string   `json:"name"`
	Keys []string `json:"keys"`
}

func (s *Server) CreateSecret(w http.ResponseWriter, r *http.Request) {
	appName := chi.URLParam(r, "name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

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
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/name":       appName,
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
	appName := chi.URLParam(r, "name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var list corev1.SecretList
	if err := s.client.List(r.Context(), &list,
		client.InNamespace(ns),
		client.MatchingLabels{
			"app.kubernetes.io/name":       appName,
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
	secretName := chi.URLParam(r, "secretName")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var secret corev1.Secret
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: secretName, Namespace: ns}, &secret); err != nil {
		writeError(w, err)
		return
	}

	// Only delete secrets managed by mortise.
	if secret.Labels["app.kubernetes.io/managed-by"] != "mortise" {
		writeJSON(w, http.StatusForbidden, errorResponse{"secret is not managed by mortise"})
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
