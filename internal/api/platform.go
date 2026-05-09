package api

import (
	"context"
	"encoding/json"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
)

// platformConfigName is the well-known singleton name.
const platformConfigName = "platform"

// patchPlatformRequest is the JSON body accepted by PATCH /api/platform.
// All fields are optional; only non-zero fields overwrite the existing value.
type patchPlatformRequest struct {
	Domain         string                      `json:"domain,omitempty"`
	ExternalDomain *string                     `json:"externalDomain,omitempty"`
	DomainTemplate *string                     `json:"domainTemplate,omitempty"`
	TLS            *patchPlatformTLS           `json:"tls,omitempty"`
	Storage        *patchPlatformStorage       `json:"storage,omitempty"`
	Registry       *patchPlatformRegistry      `json:"registry,omitempty"`
	Build          *patchPlatformBuild         `json:"build,omitempty"`
	Defaults       *patchPlatformDefaults      `json:"defaults,omitempty"`
	Observability  *patchPlatformObservability `json:"observability,omitempty"`
	GitHub         *patchPlatformGitHub        `json:"github,omitempty"`
}

type patchPlatformDefaults struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

type patchPlatformTLS struct {
	CertManagerClusterIssuer *string `json:"certManagerClusterIssuer,omitempty"`
}

type patchPlatformStorage struct {
	DefaultStorageClass string `json:"defaultStorageClass,omitempty"`
}

type patchPlatformRegistry struct {
	URL       *string `json:"url,omitempty"`
	Namespace *string `json:"namespace,omitempty"`
}

type patchPlatformBuild struct {
	BuildkitAddr    *string `json:"buildkitAddr,omitempty"`
	DefaultPlatform *string `json:"defaultPlatform,omitempty"`
}

type patchPlatformGitHub struct {
	ClientID string `json:"clientID,omitempty"`
}

type patchPlatformObservability struct {
	LogsAdapterEndpoint    *string `json:"logsAdapterEndpoint,omitempty"`
	LogsAdapterToken       *string `json:"logsAdapterToken,omitempty"`
	MetricsAdapterEndpoint *string `json:"metricsAdapterEndpoint,omitempty"`
	MetricsAdapterToken    *string `json:"metricsAdapterToken,omitempty"`
	TrafficAdapterEndpoint *string `json:"trafficAdapterEndpoint,omitempty"`
	TrafficAdapterToken    *string `json:"trafficAdapterToken,omitempty"`
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

type platformGitHubResponse struct {
	ClientID string `json:"clientID,omitempty"`
}

type platformRegistryResponse struct {
	URL       string `json:"url,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

type platformBuildResponse struct {
	BuildkitAddr    string `json:"buildkitAddr,omitempty"`
	DefaultPlatform string `json:"defaultPlatform,omitempty"`
}

type platformDefaultsResponse struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

// platformResponse is the JSON shape returned from GET and PATCH.
type platformResponse struct {
	Domain         string                              `json:"domain"`
	ExternalDomain string                              `json:"externalDomain,omitempty"`
	DomainTemplate string                              `json:"domainTemplate,omitempty"`
	TLS            mortisev1alpha1.TLSConfig           `json:"tls"`
	Storage        mortisev1alpha1.StorageConfig       `json:"storage,omitempty"`
	Registry       *platformRegistryResponse           `json:"registry,omitempty"`
	Build          *platformBuildResponse              `json:"build,omitempty"`
	Defaults       *platformDefaultsResponse           `json:"defaults,omitempty"`
	Phase          mortisev1alpha1.PlatformConfigPhase `json:"phase,omitempty"`
	Observability  *platformObservabilityResponse      `json:"observability,omitempty"`
	GitHub         *platformGitHubResponse             `json:"github,omitempty"`
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
		writeError(w, r, err)
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

	if req.Defaults != nil {
		if req.Defaults.CPU != "" {
			if _, err := resource.ParseQuantity(req.Defaults.CPU); err != nil {
				writeJSON(w, http.StatusBadRequest, errorResponse{"invalid CPU quantity: " + err.Error()})
				return
			}
		}
		if req.Defaults.Memory != "" {
			if _, err := resource.ParseQuantity(req.Defaults.Memory); err != nil {
				writeJSON(w, http.StatusBadRequest, errorResponse{"invalid memory quantity: " + err.Error()})
				return
			}
		}
	}

	if req.Observability != nil {
		if err := s.upsertAdapterTokens(r.Context(), req.Observability); err != nil {
			writeError(w, r, err)
			return
		}
	}

	var pc mortisev1alpha1.PlatformConfig
	err := s.client.Get(r.Context(), types.NamespacedName{Name: platformConfigName}, &pc)

	if errors.IsNotFound(err) {
		spec := buildPlatformSpec(mortisev1alpha1.PlatformConfigSpec{}, &req)
		pc = mortisev1alpha1.PlatformConfig{
			ObjectMeta: metav1.ObjectMeta{Name: platformConfigName},
			Spec:       spec,
		}
		if err := s.client.Create(r.Context(), &pc); err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, newPlatformResponse(&pc))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}

	// Update — merge onto existing spec (preserves build, registry, etc.).
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := s.client.Get(r.Context(), types.NamespacedName{Name: platformConfigName}, &pc); err != nil {
			return err
		}
		pc.Spec = buildPlatformSpec(pc.Spec, &req)
		return s.client.Update(r.Context(), &pc)
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newPlatformResponse(&pc))
}

// upsertAdapterTokens creates or updates the managed Secret for adapter tokens
// and sets the corresponding SecretRefs on the observability request so that
// buildPlatformSpec writes them into PlatformConfig.
func (s *Server) upsertAdapterTokens(ctx context.Context, obs *patchPlatformObservability) error {
	if obs.LogsAdapterToken == nil && obs.MetricsAdapterToken == nil && obs.TrafficAdapterToken == nil {
		return nil
	}

	key := types.NamespacedName{Namespace: adapterTokensNamespace, Name: adapterTokensSecretName}
	var secret corev1.Secret
	err := s.client.Get(ctx, key, &secret)

	if errors.IsNotFound(err) {
		if shouldDeleteAllAdapterTokens(obs) {
			return nil
		}
		secret = corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      adapterTokensSecretName,
				Namespace: adapterTokensNamespace,
				Labels:    map[string]string{"app.kubernetes.io/managed-by": "mortise"},
			},
			Data: map[string][]byte{},
		}
		if obs.LogsAdapterToken != nil && *obs.LogsAdapterToken != "" {
			secret.Data["logs"] = []byte(*obs.LogsAdapterToken)
		}
		if obs.MetricsAdapterToken != nil && *obs.MetricsAdapterToken != "" {
			secret.Data["metrics"] = []byte(*obs.MetricsAdapterToken)
		}
		if obs.TrafficAdapterToken != nil && *obs.TrafficAdapterToken != "" {
			secret.Data["traffic"] = []byte(*obs.TrafficAdapterToken)
		}
		return s.client.Create(ctx, &secret)
	}
	if err != nil {
		return err
	}

	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	if obs.LogsAdapterToken != nil {
		if *obs.LogsAdapterToken != "" {
			secret.Data["logs"] = []byte(*obs.LogsAdapterToken)
		} else {
			delete(secret.Data, "logs")
		}
	}
	if obs.MetricsAdapterToken != nil {
		if *obs.MetricsAdapterToken != "" {
			secret.Data["metrics"] = []byte(*obs.MetricsAdapterToken)
		} else {
			delete(secret.Data, "metrics")
		}
	}
	if obs.TrafficAdapterToken != nil {
		if *obs.TrafficAdapterToken != "" {
			secret.Data["traffic"] = []byte(*obs.TrafficAdapterToken)
		} else {
			delete(secret.Data, "traffic")
		}
	}
	// If all token keys have been removed, delete the orphan Secret entirely.
	if len(secret.Data) == 0 {
		return s.client.Delete(ctx, &secret)
	}
	return s.client.Update(ctx, &secret)
}

func shouldDeleteAllAdapterTokens(obs *patchPlatformObservability) bool {
	return obs.LogsAdapterToken != nil && *obs.LogsAdapterToken == "" &&
		obs.MetricsAdapterToken != nil && *obs.MetricsAdapterToken == "" &&
		obs.TrafficAdapterToken != nil && *obs.TrafficAdapterToken == ""
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
		Domain:         pc.Spec.Domain,
		ExternalDomain: pc.Spec.ExternalDomain,
		DomainTemplate: pc.Spec.DomainTemplate,
		TLS:            pc.Spec.TLS,
		Storage:        pc.Spec.Storage,
		Phase:          pc.Status.Phase,
	}
	reg := pc.Spec.Registry
	if reg.URL != "" || reg.Namespace != "" {
		resp.Registry = &platformRegistryResponse{URL: reg.URL, Namespace: reg.Namespace}
	}
	bld := pc.Spec.Build
	if bld.BuildkitAddr != "" || bld.DefaultPlatform != "" {
		resp.Build = &platformBuildResponse{BuildkitAddr: bld.BuildkitAddr, DefaultPlatform: bld.DefaultPlatform}
	}
	def := pc.Spec.Defaults
	if def.Resources.CPU != "" || def.Resources.Memory != "" {
		resp.Defaults = &platformDefaultsResponse{CPU: def.Resources.CPU, Memory: def.Resources.Memory}
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
	if pc.Spec.GitHub != nil && pc.Spec.GitHub.ClientID != "" {
		resp.GitHub = &platformGitHubResponse{ClientID: pc.Spec.GitHub.ClientID}
	}
	return resp
}

// buildPlatformSpec applies non-zero patch fields onto an existing spec.
func buildPlatformSpec(base mortisev1alpha1.PlatformConfigSpec, req *patchPlatformRequest) mortisev1alpha1.PlatformConfigSpec {
	if req.Domain != "" {
		base.Domain = req.Domain
	}
	if req.ExternalDomain != nil {
		base.ExternalDomain = *req.ExternalDomain
	}
	if req.DomainTemplate != nil {
		base.DomainTemplate = *req.DomainTemplate
	}
	if req.Defaults != nil {
		if req.Defaults.CPU != "" {
			base.Defaults.Resources.CPU = req.Defaults.CPU
		}
		if req.Defaults.Memory != "" {
			base.Defaults.Resources.Memory = req.Defaults.Memory
		}
	}
	if req.TLS != nil && req.TLS.CertManagerClusterIssuer != nil {
		base.TLS.CertManagerClusterIssuer = *req.TLS.CertManagerClusterIssuer
	}
	if req.Storage != nil {
		base.Storage.DefaultStorageClass = req.Storage.DefaultStorageClass
	}
	if req.Registry != nil {
		if req.Registry.URL != nil {
			base.Registry.URL = *req.Registry.URL
		}
		if req.Registry.Namespace != nil {
			base.Registry.Namespace = *req.Registry.Namespace
		}
	}
	if req.Build != nil {
		if req.Build.BuildkitAddr != nil {
			base.Build.BuildkitAddr = *req.Build.BuildkitAddr
		}
		if req.Build.DefaultPlatform != nil {
			base.Build.DefaultPlatform = *req.Build.DefaultPlatform
		}
	}
	if req.Observability != nil {
		if req.Observability.LogsAdapterEndpoint != nil {
			base.Observability.LogsAdapterEndpoint = *req.Observability.LogsAdapterEndpoint
		}
		if req.Observability.LogsAdapterToken != nil {
			if *req.Observability.LogsAdapterToken != "" {
				base.Observability.LogsAdapterTokenSecretRef = adapterTokenSecretRef("logs")
			} else {
				base.Observability.LogsAdapterTokenSecretRef = nil
			}
		}
		if req.Observability.MetricsAdapterEndpoint != nil {
			base.Observability.MetricsAdapterEndpoint = *req.Observability.MetricsAdapterEndpoint
		}
		if req.Observability.MetricsAdapterToken != nil {
			if *req.Observability.MetricsAdapterToken != "" {
				base.Observability.MetricsAdapterTokenSecretRef = adapterTokenSecretRef("metrics")
			} else {
				base.Observability.MetricsAdapterTokenSecretRef = nil
			}
		}
		if req.Observability.TrafficAdapterEndpoint != nil {
			base.Observability.TrafficAdapterEndpoint = *req.Observability.TrafficAdapterEndpoint
		}
		if req.Observability.TrafficAdapterToken != nil {
			if *req.Observability.TrafficAdapterToken != "" {
				base.Observability.TrafficAdapterTokenSecretRef = adapterTokenSecretRef("traffic")
			} else {
				base.Observability.TrafficAdapterTokenSecretRef = nil
			}
		}
	}
	if req.GitHub != nil {
		if base.GitHub == nil {
			base.GitHub = &mortisev1alpha1.GitHubConfig{}
		}
		base.GitHub.ClientID = req.GitHub.ClientID
	}
	return base
}
