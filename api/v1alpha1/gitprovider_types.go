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

// GitProviderType identifies the git forge.
// +kubebuilder:validation:Enum=github;gitlab;gitea
type GitProviderType string

const (
	GitProviderTypeGitHub GitProviderType = "github"
	GitProviderTypeGitLab GitProviderType = "gitlab"
	GitProviderTypeGitea  GitProviderType = "gitea"
)

// GitProviderPhase is the reconciliation phase of a GitProvider.
// +kubebuilder:validation:Enum=Pending;Ready;Failed
type GitProviderPhase string

const (
	GitProviderPhasePending GitProviderPhase = "Pending"
	GitProviderPhaseReady   GitProviderPhase = "Ready"
	GitProviderPhaseFailed  GitProviderPhase = "Failed"
)

// SecretRef is a reference to a key in a Kubernetes Secret.
type SecretRef struct {
	// Namespace of the secret.
	// +required
	Namespace string `json:"namespace"`

	// Name of the secret.
	// +required
	Name string `json:"name"`

	// Key within the secret.
	// +required
	Key string `json:"key"`
}

// OAuthConfig holds the OAuth client credentials for a git forge.
type OAuthConfig struct {
	// ClientIDSecretRef references the secret containing the OAuth client ID.
	// +required
	ClientIDSecretRef SecretRef `json:"clientIDSecretRef"`

	// ClientSecretSecretRef references the secret containing the OAuth client secret.
	// +required
	ClientSecretSecretRef SecretRef `json:"clientSecretSecretRef"`
}

// GitProviderSpec defines the desired state of GitProvider.
type GitProviderSpec struct {
	// Type is the git forge type.
	// +required
	Type GitProviderType `json:"type"`

	// Host is the base URL of the forge (e.g. https://github.com or https://gitea.internal.example).
	// +required
	Host string `json:"host"`

	// OAuth holds the OAuth application credentials used to authenticate users and
	// register webhooks on their behalf.
	// +optional
	OAuth OAuthConfig `json:"oauth,omitempty"`

	// WebhookSecretRef references the secret used to verify HMAC signatures on
	// inbound webhook payloads from this forge.
	// +optional
	WebhookSecretRef SecretRef `json:"webhookSecretRef,omitempty"`
}

// GitProviderStatus defines the observed state of GitProvider.
type GitProviderStatus struct {
	// Phase is the current lifecycle phase.
	// +optional
	Phase GitProviderPhase `json:"phase,omitempty"`

	// Conditions represent the current state of the GitProvider resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Host",type=string,JSONPath=`.spec.host`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// GitProvider is the Schema for the gitproviders API. It is cluster-scoped;
// one instance per configured git forge.
type GitProvider struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of GitProvider
	// +required
	Spec GitProviderSpec `json:"spec"`

	// status defines the observed state of GitProvider
	// +optional
	Status GitProviderStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// GitProviderList contains a list of GitProvider
type GitProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []GitProvider `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GitProvider{}, &GitProviderList{})
}
