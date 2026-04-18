package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

// platformConfigName is the well-known singleton name.
const platformConfigName = "platform"

// patchPlatformRequest is the JSON body accepted by PATCH /api/platform.
// All fields are optional; only non-zero fields overwrite the existing value.
type patchPlatformRequest struct {
	Domain  string                `json:"domain,omitempty"`
	DNS     *patchPlatformDNS     `json:"dns,omitempty"`
	TLS     *patchPlatformTLS     `json:"tls,omitempty"`
	Storage *patchPlatformStorage `json:"storage,omitempty"`
}

type patchPlatformDNS struct {
	Provider          string `json:"provider,omitempty"`
	APITokenSecretRef string `json:"apiTokenSecretRef,omitempty"`
}

type patchPlatformTLS struct {
	CertManagerClusterIssuer string `json:"certManagerClusterIssuer,omitempty"`
}

type patchPlatformStorage struct {
	DefaultStorageClass string `json:"defaultStorageClass,omitempty"`
}

// platformResponse is the JSON shape returned from GET and PATCH.
type platformResponse struct {
	Domain  string                              `json:"domain"`
	DNS     mortisev1alpha1.DNSConfig           `json:"dns"`
	TLS     mortisev1alpha1.TLSConfig           `json:"tls"`
	Storage mortisev1alpha1.StorageConfig       `json:"storage,omitempty"`
	Phase   mortisev1alpha1.PlatformConfigPhase `json:"phase,omitempty"`
}

// GetPlatform returns the current PlatformConfig.
//
// GET /api/platform
func (s *Server) GetPlatform(w http.ResponseWriter, r *http.Request) {
	var pc mortisev1alpha1.PlatformConfig
	err := s.client.Get(r.Context(), types.NamespacedName{Name: platformConfigName}, &pc)
	if errors.IsNotFound(err) {
		writeJSON(w, http.StatusOK, platformResponse{})
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, platformResponse{
		Domain:  pc.Spec.Domain,
		DNS:     pc.Spec.DNS,
		TLS:     pc.Spec.TLS,
		Storage: pc.Spec.Storage,
		Phase:   pc.Status.Phase,
	})
}

// PatchPlatform creates or updates the singleton PlatformConfig. Admin-only.
//
// PATCH /api/platform
func (s *Server) PatchPlatform(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}

	var req patchPlatformRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}

	var pc mortisev1alpha1.PlatformConfig
	err := s.client.Get(r.Context(), types.NamespacedName{Name: platformConfigName}, &pc)

	if errors.IsNotFound(err) {
		// Create.
		spec, specErr := s.buildPlatformSpec(r.Context(), mortisev1alpha1.PlatformConfigSpec{}, &req)
		if specErr != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{specErr.Error()})
			return
		}
		pc = mortisev1alpha1.PlatformConfig{
			ObjectMeta: metav1.ObjectMeta{Name: platformConfigName},
			Spec:       spec,
		}
		if err := s.client.Create(r.Context(), &pc); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, platformResponse{
			Domain:  pc.Spec.Domain,
			DNS:     pc.Spec.DNS,
			TLS:     pc.Spec.TLS,
			Storage: pc.Spec.Storage,
			Phase:   pc.Status.Phase,
		})
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}

	// Update — merge onto existing spec (preserves build, registry, etc.).
	spec, specErr := s.buildPlatformSpec(r.Context(), pc.Spec, &req)
	if specErr != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{specErr.Error()})
		return
	}
	pc.Spec = spec
	if err := s.client.Update(r.Context(), &pc); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, platformResponse{
		Domain:  pc.Spec.Domain,
		DNS:     pc.Spec.DNS,
		TLS:     pc.Spec.TLS,
		Storage: pc.Spec.Storage,
		Phase:   pc.Status.Phase,
	})
}

// buildPlatformSpec applies non-zero patch fields onto an existing spec.
// It creates k8s Secrets as needed (e.g. for DNS API tokens passed as raw values).
func (s *Server) buildPlatformSpec(ctx context.Context, base mortisev1alpha1.PlatformConfigSpec, req *patchPlatformRequest) (mortisev1alpha1.PlatformConfigSpec, error) {
	if req.Domain != "" {
		base.Domain = req.Domain
	}
	if req.DNS != nil {
		if req.DNS.Provider != "" {
			base.DNS.Provider = mortisev1alpha1.DNSProviderType(req.DNS.Provider)
		}
		if req.DNS.APITokenSecretRef != "" {
			// Create or update a Secret from the raw token value, then reference it.
			secretName := "platform-dns-token"
			if err := s.ensureSecret(ctx, secretName, "token", req.DNS.APITokenSecretRef); err != nil {
				return base, fmt.Errorf("create DNS token secret: %w", err)
			}
			base.DNS.APITokenSecretRef = mortisev1alpha1.SecretRef{
				Namespace: "mortise-system",
				Name:      secretName,
				Key:       "token",
			}
		}
	}
	if req.TLS != nil && req.TLS.CertManagerClusterIssuer != "" {
		base.TLS.CertManagerClusterIssuer = req.TLS.CertManagerClusterIssuer
	}
	if req.Storage != nil {
		base.Storage.DefaultStorageClass = req.Storage.DefaultStorageClass
	}
	return base, nil
}

// ensureSecret creates or updates a Secret in mortise-system with a single key/value.
func (s *Server) ensureSecret(ctx context.Context, name, key, value string) error {
	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "mortise-system",
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "mortise"},
		},
		Data: map[string][]byte{key: []byte(value)},
	}
	var existing corev1.Secret
	err := s.client.Get(ctx, types.NamespacedName{Namespace: "mortise-system", Name: name}, &existing)
	if errors.IsNotFound(err) {
		return s.client.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	existing.Data = desired.Data
	return s.client.Update(ctx, &existing)
}
