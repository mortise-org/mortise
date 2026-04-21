package api

import (
	"encoding/json"
	"net/http"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/authz"
)

// platformConfigName is the well-known singleton name.
const platformConfigName = "platform"

// patchPlatformRequest is the JSON body accepted by PATCH /api/platform.
// All fields are optional; only non-zero fields overwrite the existing value.
type patchPlatformRequest struct {
	Domain   string                 `json:"domain,omitempty"`
	TLS      *patchPlatformTLS      `json:"tls,omitempty"`
	Storage  *patchPlatformStorage  `json:"storage,omitempty"`
	Registry *patchPlatformRegistry `json:"registry,omitempty"`
	Build    *patchPlatformBuild    `json:"build,omitempty"`
}

type patchPlatformTLS struct {
	CertManagerClusterIssuer string `json:"certManagerClusterIssuer,omitempty"`
}

type patchPlatformStorage struct {
	DefaultStorageClass string `json:"defaultStorageClass,omitempty"`
}

type patchPlatformRegistry struct {
	URL       string `json:"url,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

type patchPlatformBuild struct {
	BuildkitAddr    string `json:"buildkitAddr,omitempty"`
	DefaultPlatform string `json:"defaultPlatform,omitempty"`
}

// platformResponse is the JSON shape returned from GET and PATCH.
type platformResponse struct {
	Domain  string                              `json:"domain"`
	TLS     mortisev1alpha1.TLSConfig           `json:"tls"`
	Storage mortisev1alpha1.StorageConfig       `json:"storage,omitempty"`
	Phase   mortisev1alpha1.PlatformConfigPhase `json:"phase,omitempty"`
}

// GetPlatform returns the current PlatformConfig.
//
// GET /api/platform
func (s *Server) GetPlatform(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, authz.Resource{Kind: "platform", Name: "platform"}, authz.ActionRead) {
		return
	}
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
		TLS:     pc.Spec.TLS,
		Storage: pc.Spec.Storage,
		Phase:   pc.Status.Phase,
	})
}

// PatchPlatform creates or updates the singleton PlatformConfig. Admin-only.
//
// PATCH /api/platform
func (s *Server) PatchPlatform(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, authz.Resource{Kind: "platform", Name: "platform"}, authz.ActionUpdate) {
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
		spec := buildPlatformSpec(mortisev1alpha1.PlatformConfigSpec{}, &req)
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
	pc.Spec = buildPlatformSpec(pc.Spec, &req)
	if err := s.client.Update(r.Context(), &pc); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, platformResponse{
		Domain:  pc.Spec.Domain,
		TLS:     pc.Spec.TLS,
		Storage: pc.Spec.Storage,
		Phase:   pc.Status.Phase,
	})
}

// buildPlatformSpec applies non-zero patch fields onto an existing spec.
func buildPlatformSpec(base mortisev1alpha1.PlatformConfigSpec, req *patchPlatformRequest) mortisev1alpha1.PlatformConfigSpec {
	if req.Domain != "" {
		base.Domain = req.Domain
	}
	if req.TLS != nil && req.TLS.CertManagerClusterIssuer != "" {
		base.TLS.CertManagerClusterIssuer = req.TLS.CertManagerClusterIssuer
	}
	if req.Storage != nil {
		base.Storage.DefaultStorageClass = req.Storage.DefaultStorageClass
	}
	if req.Registry != nil {
		if req.Registry.URL != "" {
			base.Registry.URL = req.Registry.URL
		}
		if req.Registry.Namespace != "" {
			base.Registry.Namespace = req.Registry.Namespace
		}
	}
	if req.Build != nil {
		if req.Build.BuildkitAddr != "" {
			base.Build.BuildkitAddr = req.Build.BuildkitAddr
		}
		if req.Build.DefaultPlatform != "" {
			base.Build.DefaultPlatform = req.Build.DefaultPlatform
		}
	}
	return base
}
