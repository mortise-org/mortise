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

// ProjectRole is the role a member holds within a project.
// +kubebuilder:validation:Enum=owner;developer;viewer
type ProjectRole string

const (
	ProjectRoleOwner     ProjectRole = "owner"
	ProjectRoleDeveloper ProjectRole = "developer"
	ProjectRoleViewer    ProjectRole = "viewer"
)

// ProjectMemberSpec defines the desired state of a ProjectMember.
type ProjectMemberSpec struct {
	// Email is the platform user's email address.
	// +kubebuilder:validation:Required
	Email string `json:"email"`

	// Project is the name of the owning Project (denormalized for queries).
	// +kubebuilder:validation:Required
	Project string `json:"project"`

	// Role is the member's project-level role.
	// +kubebuilder:validation:Required
	Role ProjectRole `json:"role"`
}

// ProjectMemberStatus defines the observed state of a ProjectMember.
type ProjectMemberStatus struct {
	// AddedAt is the timestamp when the member was added.
	// +optional
	AddedAt string `json:"addedAt,omitempty"`

	// AddedBy is the email of the user who added this member.
	// +optional
	AddedBy string `json:"addedBy,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Email",type=string,JSONPath=`.spec.email`
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.role`
// +kubebuilder:printcolumn:name="Project",type=string,JSONPath=`.spec.project`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ProjectMember binds a platform user to a Project with a specific role.
// Lives in the Project's control namespace (pj-{project}).
type ProjectMember struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec ProjectMemberSpec `json:"spec"`

	// +optional
	Status ProjectMemberStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ProjectMemberList contains a list of ProjectMember.
type ProjectMemberList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ProjectMember `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ProjectMember{}, &ProjectMemberList{})
}
