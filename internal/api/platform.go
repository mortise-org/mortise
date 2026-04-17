package api

import (
	"encoding/json"
	"net/http"

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
	Domain string            `json:"domain,omitempty"`
	DNS    *patchPlatformDNS `json:"dns,omitempty"`
	TLS    *patchPlatformTLS `json:"tls,omitempty"`
}

type patchPlatformDNS struct {
	Provider          string `json:"provider,omitempty"`
	APITokenSecretRef string `json:"apiTokenSecretRef,omitempty"`
}

type patchPlatformTLS struct {
	CertManagerClusterIssuer string `json:"certManagerClusterIssuer,omitempty"`
}

// platformResponse is the JSON shape returned from GET and PATCH.
type platformResponse struct {
	Domain string                              `json:"domain"`
	DNS    mortisev1alpha1.DNSConfig           `json:"dns"`
	TLS    mortisev1alpha1.TLSConfig           `json:"tls"`
	Phase  mortisev1alpha1.PlatformConfigPhase `json:"phase,omitempty"`
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
		Domain: pc.Spec.Domain,
		DNS:    pc.Spec.DNS,
		TLS:    pc.Spec.TLS,
		Phase:  pc.Status.Phase,
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
		pc = mortisev1alpha1.PlatformConfig{
			ObjectMeta: metav1.ObjectMeta{Name: platformConfigName},
			Spec:       buildPlatformSpec(mortisev1alpha1.PlatformConfigSpec{}, &req),
		}
		if err := s.client.Create(r.Context(), &pc); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, platformResponse{
			Domain: pc.Spec.Domain,
			DNS:    pc.Spec.DNS,
			TLS:    pc.Spec.TLS,
			Phase:  pc.Status.Phase,
		})
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}

	// Update.
	pc.Spec = buildPlatformSpec(pc.Spec, &req)
	if err := s.client.Update(r.Context(), &pc); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, platformResponse{
		Domain: pc.Spec.Domain,
		DNS:    pc.Spec.DNS,
		TLS:    pc.Spec.TLS,
		Phase:  pc.Status.Phase,
	})
}

// buildPlatformSpec applies non-zero patch fields onto an existing spec.
func buildPlatformSpec(base mortisev1alpha1.PlatformConfigSpec, req *patchPlatformRequest) mortisev1alpha1.PlatformConfigSpec {
	if req.Domain != "" {
		base.Domain = req.Domain
	}
	if req.DNS != nil {
		if req.DNS.Provider != "" {
			base.DNS.Provider = mortisev1alpha1.DNSProviderType(req.DNS.Provider)
		}
		if req.DNS.APITokenSecretRef != "" {
			base.DNS.APITokenSecretRef = mortisev1alpha1.SecretRef{
				Namespace: "mortise-system",
				Name:      req.DNS.APITokenSecretRef,
				Key:       "token",
			}
		}
	}
	if req.TLS != nil && req.TLS.CertManagerClusterIssuer != "" {
		base.TLS.CertManagerClusterIssuer = req.TLS.CertManagerClusterIssuer
	}
	return base
}
