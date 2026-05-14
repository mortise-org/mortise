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
// +kubebuilder:validation:Enum=Pending;Ready;Failed
type PreviewPhase string

const (
	PreviewPhasePending PreviewPhase = "Pending"
	PreviewPhaseReady   PreviewPhase = "Ready"
	PreviewPhaseFailed  PreviewPhase = "Failed"
)

// PullRequestRef identifies the PR that triggered this preview.
type PullRequestRef struct {
	Number int    `json:"number"`
	Branch string `json:"branch"`
	SHA    string `json:"sha"`
}

// PreviewEnvironmentSpec defines the desired state of PreviewEnvironment.
type PreviewEnvironmentSpec struct {
	// ProjectRef is the name of the parent Project (cluster-scoped).
	// +kubebuilder:validation:Required
	ProjectRef string `json:"projectRef"`

	// SourceEnv is the project environment name to clone from.
	// +kubebuilder:validation:Required
	SourceEnv string `json:"sourceEnv"`

	// PullRequest identifies the PR that triggered this preview.
	// +kubebuilder:validation:Required
	PullRequest PullRequestRef `json:"pullRequest"`
}

// PreviewEnvironmentStatus defines the observed state of PreviewEnvironment.
type PreviewEnvironmentStatus struct {
	// Phase is the current lifecycle phase.
	Phase PreviewPhase `json:"phase,omitempty"`

	// EnvironmentName is the name of the created ProjectEnvironment (e.g. "pr-42").
	// +optional
	EnvironmentName string `json:"environmentName,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="PR",type=integer,JSONPath=`.spec.pullRequest.number`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Environment",type=string,JSONPath=`.status.environmentName`
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
