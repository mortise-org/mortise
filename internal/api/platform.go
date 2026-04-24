package api

import (
	"context"
	"encoding/json"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
)

// platformConfigName is the well-known singleton name.
const platformConfigName = "platform"

// patchPlatformRequest is the JSON body accepted by PATCH /api/platform.
// All fields are optional; only non-zero fields overwrite the existing value.
type patchPlatformRequest struct {
	Domain        string                      `json:"domain,omitempty"`
	TLS           *patchPlatformTLS           `json:"tls,omitempty"`
	Storage       *patchPlatformStorage       `json:"storage,omitempty"`
	Registry      *patchPlatformRegistry      `json:"registry,omitempty"`
	Build         *patchPlatformBuild         `json:"build,omitempty"`
	Observability *patchPlatformObservability `json:"observability,omitempty"`
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

type patchPlatformObservability struct {
	LogsAdapterEndpoint    string `json:"logsAdapterEndpoint,omitempty"`
	LogsAdapterToken       string `json:"logsAdapterToken,omitempty"`
	MetricsAdapterEndpoint string `json:"metricsAdapterEndpoint,omitempty"`
	MetricsAdapterToken    string `json:"metricsAdapterToken,omitempty"`
	TrafficAdapterEndpoint string `json:"trafficAdapterEndpoint,omitempty"`
	TrafficAdapterToken    string `json:"trafficAdapterToken,omitempty"`
}

type platformObservabilityResponse struct {
	LogsAdapterEndpoint    string `json:"logsAdapterEndpoint,omitempty"`
	HasLogsToken           bool   `json:"hasLogsToken,omitempty"`
	MetricsAdapterEndpoint string `json:"metricsAdapterEndpoint,omitempty"`
	HasMetricsToken        bool   `json:"hasMetricsToken,omitempty"`
	TrafficAdapterEndpoint string `json:"trafficAdapterEndpoint,omitempty"`
	HasTrafficToken        bool   `json:"hasTrafficToken,omitempty"`
}

const (
	adapterTokensSecretName = "observer-adapter-tokens"
	adapterTokensNamespace  = "mortise-system"
)

// platformResponse is the JSON shape returned from GET and PATCH.
type platformResponse struct {
	Domain        string                              `json:"domain"`
	TLS           mortisev1alpha1.TLSConfig           `json:"tls"`
	Storage       mortisev1alpha1.StorageConfig       `json:"storage,omitempty"`
	Phase         mortisev1alpha1.PlatformConfigPhase `json:"phase,omitempty"`
	Observability *platformObservabilityResponse      `json:"observability,omitempty"`
}

// GetPlatform returns the current PlatformConfig.
//
// GET /api/platform
//
// @Summary Get platform configuration
// @Description Returns the current PlatformConfig singleton
// @Tags platform
// @Produce json
// @Security BearerAuth
// @Success 200 {object} platformResponse
// @Failure 403 {object} errorResponse
// @Router /platform [get]
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

	writeJSON(w, http.StatusOK, newPlatformResponse(&pc))
}

// PatchPlatform creates or updates the singleton PlatformConfig. Admin-only.
//
// PATCH /api/platform
//
// @Summary Update platform configuration
// @Description Creates or updates the singleton PlatformConfig. Admin-only.
// @Tags platform
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body patchPlatformRequest true "Platform config fields to update"
// @Success 200 {object} platformResponse
// @Success 201 {object} platformResponse
// @Failure 400 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Router /platform [patch]
func (s *Server) PatchPlatform(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, authz.Resource{Kind: "platform", Name: "platform"}, authz.ActionUpdate) {
		return
	}

	var req patchPlatformRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}

	if req.Observability != nil {
		if err := s.upsertAdapterTokens(r.Context(), req.Observability); err != nil {
			writeError(w, err)
			return
		}
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
		writeJSON(w, http.StatusCreated, newPlatformResponse(&pc))
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
	writeJSON(w, http.StatusOK, newPlatformResponse(&pc))
}

// upsertAdapterTokens creates or updates the managed Secret for adapter tokens
// and sets the corresponding SecretRefs on the observability request so that
// buildPlatformSpec writes them into PlatformConfig.
func (s *Server) upsertAdapterTokens(ctx context.Context, obs *patchPlatformObservability) error {
	if obs.LogsAdapterToken == "" && obs.MetricsAdapterToken == "" && obs.TrafficAdapterToken == "" {
		return nil
	}

	key := types.NamespacedName{Namespace: adapterTokensNamespace, Name: adapterTokensSecretName}
	var secret corev1.Secret
	err := s.client.Get(ctx, key, &secret)

	if errors.IsNotFound(err) {
		secret = corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      adapterTokensSecretName,
				Namespace: adapterTokensNamespace,
				Labels:    map[string]string{"app.kubernetes.io/managed-by": "mortise"},
			},
			Data: map[string][]byte{},
		}
		if obs.LogsAdapterToken != "" {
			secret.Data["logs"] = []byte(obs.LogsAdapterToken)
		}
		if obs.MetricsAdapterToken != "" {
			secret.Data["metrics"] = []byte(obs.MetricsAdapterToken)
		}
		if obs.TrafficAdapterToken != "" {
			secret.Data["traffic"] = []byte(obs.TrafficAdapterToken)
		}
		return s.client.Create(ctx, &secret)
	}
	if err != nil {
		return err
	}

	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	if obs.LogsAdapterToken != "" {
		secret.Data["logs"] = []byte(obs.LogsAdapterToken)
	}
	if obs.MetricsAdapterToken != "" {
		secret.Data["metrics"] = []byte(obs.MetricsAdapterToken)
	}
	if obs.TrafficAdapterToken != "" {
		secret.Data["traffic"] = []byte(obs.TrafficAdapterToken)
	}
	return s.client.Update(ctx, &secret)
}

func adapterTokenSecretRef(key string) *mortisev1alpha1.SecretRef {
	return &mortisev1alpha1.SecretRef{
		Namespace: adapterTokensNamespace,
		Name:      adapterTokensSecretName,
		Key:       key,
	}
}

func newPlatformResponse(pc *mortisev1alpha1.PlatformConfig) platformResponse {
	resp := platformResponse{
		Domain:  pc.Spec.Domain,
		TLS:     pc.Spec.TLS,
		Storage: pc.Spec.Storage,
		Phase:   pc.Status.Phase,
	}
	obs := pc.Spec.Observability
	if obs.LogsAdapterEndpoint != "" || obs.MetricsAdapterEndpoint != "" || obs.TrafficAdapterEndpoint != "" ||
		obs.LogsAdapterTokenSecretRef != nil || obs.MetricsAdapterTokenSecretRef != nil || obs.TrafficAdapterTokenSecretRef != nil {
		resp.Observability = &platformObservabilityResponse{
			LogsAdapterEndpoint:    obs.LogsAdapterEndpoint,
			HasLogsToken:           obs.LogsAdapterTokenSecretRef != nil,
			MetricsAdapterEndpoint: obs.MetricsAdapterEndpoint,
			HasMetricsToken:        obs.MetricsAdapterTokenSecretRef != nil,
			TrafficAdapterEndpoint: obs.TrafficAdapterEndpoint,
			HasTrafficToken:        obs.TrafficAdapterTokenSecretRef != nil,
		}
	}
	return resp
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
	if req.Observability != nil {
		if req.Observability.LogsAdapterEndpoint != "" {
			base.Observability.LogsAdapterEndpoint = req.Observability.LogsAdapterEndpoint
		}
		if req.Observability.LogsAdapterToken != "" {
			base.Observability.LogsAdapterTokenSecretRef = adapterTokenSecretRef("logs")
		}
		if req.Observability.MetricsAdapterEndpoint != "" {
			base.Observability.MetricsAdapterEndpoint = req.Observability.MetricsAdapterEndpoint
		}
		if req.Observability.MetricsAdapterToken != "" {
			base.Observability.MetricsAdapterTokenSecretRef = adapterTokenSecretRef("metrics")
		}
		if req.Observability.TrafficAdapterEndpoint != "" {
			base.Observability.TrafficAdapterEndpoint = req.Observability.TrafficAdapterEndpoint
		}
		if req.Observability.TrafficAdapterToken != "" {
			base.Observability.TrafficAdapterTokenSecretRef = adapterTokenSecretRef("traffic")
		}
	}
	return base
}
