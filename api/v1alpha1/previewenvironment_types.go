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

// PreviewPhase represents the lifecycle phase of a preview environment.
// +kubebuilder:validation:Enum=Pending;Building;Ready;Failed;Expired
type PreviewPhase string

const (
	PreviewPhasePending  PreviewPhase = "Pending"
	PreviewPhaseBuilding PreviewPhase = "Building"
	PreviewPhaseReady    PreviewPhase = "Ready"
	PreviewPhaseFailed   PreviewPhase = "Failed"
	PreviewPhaseExpired  PreviewPhase = "Expired"
)

// PullRequestRef identifies the PR that triggered this preview.
type PullRequestRef struct {
	Number int    `json:"number"`
	Branch string `json:"branch"`
	SHA    string `json:"sha"`
}

// PreviewEnvironmentSpec defines the desired state of PreviewEnvironment.
type PreviewEnvironmentSpec struct {
	// AppRef is the name of the parent App this previews (same namespace).
	// +kubebuilder:validation:Required
	AppRef string `json:"appRef"`

	// PullRequest identifies the PR that triggered this preview.
	// +kubebuilder:validation:Required
	PullRequest PullRequestRef `json:"pullRequest"`

	// Replicas for the preview Deployment. Inherited from staging, overridable.
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Resources for the preview Deployment. Inherited from staging, overridable.
	// +optional
	Resources ResourceRequirements `json:"resources,omitempty"`

	// Env vars for the preview Deployment. Inherited from staging, overridable.
	// +optional
	Env []EnvVar `json:"env,omitempty"`

	// Bindings inherited from the staging environment.
	// +optional
	Bindings []Binding `json:"bindings,omitempty"`

	// Domain for this preview (resolved from App.spec.preview.domain template).
	// +optional
	Domain string `json:"domain,omitempty"`

	// TTL after which the preview is auto-deleted if the PR is still open.
	// +optional
	TTL metav1.Duration `json:"ttl,omitempty"`
}

// PreviewEnvironmentStatus defines the observed state of PreviewEnvironment.
type PreviewEnvironmentStatus struct {
	// Phase is the current lifecycle phase.
	Phase PreviewPhase `json:"phase,omitempty"`

	// URL is the HTTPS endpoint for the preview.
	URL string `json:"url,omitempty"`

	// Image is the built container image reference.
	Image string `json:"image,omitempty"`

	// ExpiresAt is when this preview will be auto-deleted.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="App",type=string,JSONPath=`.spec.appRef`
// +kubebuilder:printcolumn:name="PR",type=integer,JSONPath=`.spec.pullRequest.number`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PreviewEnvironment is the Schema for the previewenvironments API
type PreviewEnvironment struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of PreviewEnvironment
	// +required
	Spec PreviewEnvironmentSpec `json:"spec"`

	// status defines the observed state of PreviewEnvironment
	// +optional
	Status PreviewEnvironmentStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// PreviewEnvironmentList contains a list of PreviewEnvironment
type PreviewEnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []PreviewEnvironment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PreviewEnvironment{}, &PreviewEnvironmentList{})
}
