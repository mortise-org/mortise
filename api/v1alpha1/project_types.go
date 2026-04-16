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

// ProjectSpec defines the desired state of a Project — the top-level grouping
// above Apps. Each Project owns a Kubernetes namespace named
// `project-{metadata.name}` into which its Apps are placed.
type ProjectSpec struct {
	// Description is a short, human-readable note about the project.
	// +optional
	Description string `json:"description,omitempty"`

	// Future fields (v2+):
	// - Team      string  — per-project team/ownership
	// - Quota     Quota   — CPU/memory/storage caps per project
	// - DomainSuffix string — override platform default domain
	// - Retention Retention — preview env / build cache retention policy
}

// ProjectPhase represents the lifecycle phase of a Project.
// +kubebuilder:validation:Enum=Pending;Ready;Terminating;Failed
type ProjectPhase string

const (
	ProjectPhasePending     ProjectPhase = "Pending"
	ProjectPhaseReady       ProjectPhase = "Ready"
	ProjectPhaseTerminating ProjectPhase = "Terminating"
	ProjectPhaseFailed      ProjectPhase = "Failed"
)

// ProjectStatus defines the observed state of a Project.
type ProjectStatus struct {
	// Phase is the overall lifecycle phase.
	// +optional
	Phase ProjectPhase `json:"phase,omitempty"`

	// Namespace is the name of the Kubernetes namespace backing this Project.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// AppCount is the number of Apps currently inside this Project's namespace.
	// +optional
	AppCount int32 `json:"appCount,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.status.namespace`
// +kubebuilder:printcolumn:name="Apps",type=integer,JSONPath=`.status.appCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Project is the Schema for the projects API. It is cluster-scoped; deleting a
// Project cascades to its namespace and every App inside.
type Project struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec ProjectSpec `json:"spec"`

	// +optional
	Status ProjectStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ProjectList contains a list of Project.
type ProjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Project `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Project{}, &ProjectList{})
}
