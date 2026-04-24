/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PlatformConfigPhase is the reconciliation phase of a PlatformConfig.
// +kubebuilder:validation:Enum=Pending;Ready;Failed
type PlatformConfigPhase string

const (
	PlatformConfigPhasePending PlatformConfigPhase = "Pending"
	PlatformConfigPhaseReady   PlatformConfigPhase = "Ready"
	PlatformConfigPhaseFailed  PlatformConfigPhase = "Failed"
)

// StorageConfig holds platform-level storage settings.
type StorageConfig struct {
	// DefaultStorageClass is the StorageClass to use for App volumes that do not
	// specify their own. If empty, the cluster default StorageClass is used.
	// +optional
	DefaultStorageClass string `json:"defaultStorageClass,omitempty"`
}

// RegistryConfig holds the OCI registry configuration used for built images.
type RegistryConfig struct {
	// URL is the OCI registry endpoint (e.g. registry.example.com).
	// +required
	URL string `json:"url"`

	// Namespace is the registry namespace under which app images are stored.
	// Defaults to "mortise".
	// +optional
	// +kubebuilder:default=mortise
	Namespace string `json:"namespace,omitempty"`

	// CredentialsSecretRef references a secret containing Basic or Bearer auth
	// credentials for the registry (keys: username, password).
	// +optional
	CredentialsSecretRef *SecretRef `json:"credentialsSecretRef,omitempty"`

	// PullSecretName is the name of the k8s image-pull Secret that carries the
	// registry credentials and is projected into App pods.
	// +optional
	PullSecretName string `json:"pullSecretName,omitempty"`

	// InsecureSkipTLSVerify disables TLS verification when talking to the registry.
	// Intended for local k3d clusters only.
	// +optional
	InsecureSkipTLSVerify bool `json:"insecureSkipTLSVerify,omitempty"`

	// PullURL is the registry URL that kubelet uses to pull images. When the
	// bundled registry runs behind a node-local DaemonSet proxy, this differs
	// from URL (which is used for BuildKit pushes via cluster DNS). If empty,
	// URL is used for both push and pull.
	// +optional
	PullURL string `json:"pullURL,omitempty"`
}

// BuildConfig holds the BuildKit configuration.
type BuildConfig struct {
	// BuildkitAddr is the address of the BuildKit daemon
	// (e.g. tcp://buildkitd:1234 or unix:///run/buildkit/buildkitd.sock).
	// +required
	BuildkitAddr string `json:"buildkitAddr"`

	// TLSSecretRef references a secret containing mTLS credentials for BuildKit
	// (keys: ca.crt, tls.crt, tls.key).
	// +optional
	TLSSecretRef *SecretRef `json:"tlsSecretRef,omitempty"`

	// DefaultPlatform is the OCI platform string used when building images
	// (e.g. linux/amd64). Defaults to linux/amd64.
	// +optional
	// +kubebuilder:default="linux/amd64"
	DefaultPlatform string `json:"defaultPlatform,omitempty"`
}

// TLSConfig holds TLS/cert-manager configuration.
type TLSConfig struct {
	// CertManagerClusterIssuer is the name of the cert-manager ClusterIssuer to
	// use when provisioning TLS certificates for App Ingresses
	// (e.g. letsencrypt-prod).
	// +optional
	CertManagerClusterIssuer string `json:"certManagerClusterIssuer,omitempty"`
}

// DefaultsConfig holds platform-wide defaults applied to Apps that do not
// specify their own values.
type DefaultsConfig struct {
	// Resources are the default CPU and memory limits for App containers.
	// +optional
	Resources ResourceRequirements `json:"resources,omitempty"`
}

// PlatformConfigSpec defines the desired state of PlatformConfig.
type PlatformConfigSpec struct {
	// Domain is the base domain for the platform. Apps receive subdomains under
	// this domain automatically (e.g. yourdomain.com → app.yourdomain.com).
	// When empty, apps are only reachable via port-forward or ClusterIP.
	// +optional
	Domain string `json:"domain,omitempty"`

	// Storage configures platform-level storage defaults.
	// +optional
	Storage StorageConfig `json:"storage,omitempty"`

	// Registry configures the OCI registry used to store built App images.
	// +optional
	Registry RegistryConfig `json:"registry,omitempty"`

	// Build configures the BuildKit daemon used to build App images from source.
	// +optional
	Build BuildConfig `json:"build,omitempty"`

	// TLS configures TLS certificate issuance for App Ingresses.
	// +optional
	TLS TLSConfig `json:"tls,omitempty"`

	// GitHub holds optional overrides for the project-maintained GitHub App.
	// Admins who want to use their own GitHub OAuth App can specify the client
	// ID here; otherwise the device flow uses the built-in default.
	// +optional
	GitHub *GitHubConfig `json:"github,omitempty"`

	// Observability configures external adapter endpoints for historical logs
	// and metrics queries.
	// +optional
	Observability ObservabilitySpec `json:"observability,omitempty"`

	// Defaults holds platform-wide defaults for Apps.
	// +optional
	Defaults DefaultsConfig `json:"defaults,omitempty"`
}

// GitHubConfig holds optional GitHub OAuth App overrides.
type GitHubConfig struct {
	// ClientID is the OAuth client ID for a self-hosted GitHub App.
	// When set, the device flow uses this instead of the project-maintained default.
	// +optional
	ClientID string `json:"clientID,omitempty"`
}

// ObservabilitySpec configures external adapter endpoints for historical log
// queries and metrics. When set, the API proxies requests to the adapter;
// when absent, the corresponding UI features degrade gracefully.
type ObservabilitySpec struct {
	// LogsAdapterEndpoint is the base URL of a service implementing the
	// Mortise log adapter contract (GET /v1/logs).
	// +optional
	LogsAdapterEndpoint string `json:"logsAdapterEndpoint,omitempty"`

	// LogsAdapterTokenSecretRef references a Secret containing a bearer token
	// for authenticating with the logs adapter.
	// +optional
	LogsAdapterTokenSecretRef *SecretRef `json:"logsAdapterTokenSecretRef,omitempty"`

	// MetricsAdapterEndpoint is the base URL of a service implementing the
	// Mortise metrics adapter contract (GET /v1/metrics).
	// +optional
	MetricsAdapterEndpoint string `json:"metricsAdapterEndpoint,omitempty"`

	// MetricsAdapterTokenSecretRef references a Secret containing a bearer token
	// for authenticating with the metrics adapter.
	// +optional
	MetricsAdapterTokenSecretRef *SecretRef `json:"metricsAdapterTokenSecretRef,omitempty"`

	// TrafficAdapterEndpoint is the base URL of a service implementing the
	// Mortise traffic adapter contract (GET /v1/traffic).
	// +optional
	TrafficAdapterEndpoint string `json:"trafficAdapterEndpoint,omitempty"`

	// TrafficAdapterTokenSecretRef references a Secret containing a bearer token
	// for authenticating with the traffic adapter.
	// +optional
	TrafficAdapterTokenSecretRef *SecretRef `json:"trafficAdapterTokenSecretRef,omitempty"`
}

// PlatformConfigStatus defines the observed state of PlatformConfig.
type PlatformConfigStatus struct {
	// Phase is the current lifecycle phase.
	// +optional
	Phase PlatformConfigPhase `json:"phase,omitempty"`

	// Conditions represent the current state of the PlatformConfig resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Domain",type=string,JSONPath=`.spec.domain`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PlatformConfig is the Schema for the platformconfigs API. It is cluster-scoped
// and there must be exactly one instance named "platform" per cluster.
type PlatformConfig struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of PlatformConfig
	// +required
	Spec PlatformConfigSpec `json:"spec"`

	// status defines the observed state of PlatformConfig
	// +optional
	Status PlatformConfigStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// PlatformConfigList contains a list of PlatformConfig
type PlatformConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []PlatformConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PlatformConfig{}, &PlatformConfigList{})
}
