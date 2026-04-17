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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const deployTokenPrefix = "mrt_"

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
	Environment string `json:"environment"`
	CreatedAt   string `json:"createdAt,omitempty"`
}

// CreateToken generates a deploy token, stores its hash as a k8s Secret, and
// returns the raw token value once.
func (s *Server) CreateToken(w http.ResponseWriter, r *http.Request) {
	ns, ok := s.resolveProject(w, r)
	if !ok {
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

	writeJSON(w, http.StatusCreated, tokenResponse{
		Token:       token,
		Name:        req.Name,
		Environment: req.Environment,
	})
}

// ListTokens returns metadata for all deploy tokens scoped to an app.
func (s *Server) ListTokens(w http.ResponseWriter, r *http.Request) {
	ns, ok := s.resolveProject(w, r)
	if !ok {
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
func (s *Server) DeleteToken(w http.ResponseWriter, r *http.Request) {
	ns, ok := s.resolveProject(w, r)
	if !ok {
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

	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// validateDeployToken checks whether an mrt_ bearer token is valid for the
// given app and environment. Returns true if the token is valid.
func (s *Server) validateDeployToken(r *http.Request, ns, appName, env string) bool {
	header := r.Header.Get("Authorization")
	if header == "" || !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if !strings.HasPrefix(token, deployTokenPrefix) {
		return false
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
		return false
	}

	for i := range list.Items {
		stored := string(list.Items[i].Data["token-hash"])
		if stored == hashHex {
			return true
		}
	}
	return false
}
