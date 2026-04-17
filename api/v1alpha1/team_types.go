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

// TeamSpec defines the desired state of a Team.
//
// v1 intentionally ships only the singleton `default-team` auto-created at
// first-run setup. Every user is implicitly bound to it; the UI surfaces no
// team chrome. The CRD exists so v2 (multi-team installs, per-team roles,
// env-scoped grants) is additive rather than a data migration.
type TeamSpec struct {
	// Description is a short, human-readable note about the team.
	// +optional
	Description string `json:"description,omitempty"`
}

// TeamPhase represents the lifecycle phase of a Team.
// +kubebuilder:validation:Enum=Pending;Ready;Failed
type TeamPhase string

const (
	TeamPhasePending TeamPhase = "Pending"
	TeamPhaseReady   TeamPhase = "Ready"
	TeamPhaseFailed  TeamPhase = "Failed"
)

// TeamStatus defines the observed state of a Team.
type TeamStatus struct {
	// Phase is the overall lifecycle phase.
	// +optional
	Phase TeamPhase `json:"phase,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Team is the Schema for the teams API. v1 runs with one singleton
// `default-team`; the reconciler rejects any other name. See SPEC.md §5.10
// for the v1→v2 forward-compat rationale.
type Team struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec TeamSpec `json:"spec"`

	// +optional
	Status TeamStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// TeamList contains a list of Team.
type TeamList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Team `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Team{}, &TeamList{})
}
