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

// PreviewConfig is the project-level PR environments toggle.
// When Enabled=true, opening a PR creates a clone of the source
// environment with git-source apps pointed at the PR branch.
type PreviewConfig struct {
	// Enabled turns PR environments on for this Project.
	Enabled bool `json:"enabled,omitempty"`

	// SourceEnvironment is the project environment name to clone previews from.
	// If empty, auto-resolves: prefers "staging", falls back to first non-production env.
	// Must not reference a restricted environment.
	// +optional
	SourceEnvironment string `json:"sourceEnvironment,omitempty"`

	// BotPR controls whether PRs opened by bot accounts spawn previews.
	// Defaults to true — all PRs get previews regardless of author.
	// +optional
	BotPR *bool `json:"botPR,omitempty"`
}

// ProjectEnvironment declares a named deployment environment that belongs to
// the Project. Every App in the Project auto-exists in every ProjectEnvironment;
// `App.Spec.Environments[]` carries only per-env overrides (resources, env vars,
// domain, etc.) and an optional `enabled` opt-out for that App × env pair.
type ProjectEnvironment struct {
	// Name is the environment's DNS-label identifier (e.g. "production",
	// "staging"). Referenced from App overrides, navbar selector, preview envs,
	// and resource labels (`mortise.dev/environment`).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// DisplayOrder controls the UI sort order in the navbar env selector and
	// project settings list. Lower values appear first; ties fall back to
	// creation order.
	// +optional
	DisplayOrder int `json:"displayOrder,omitempty"`

	// Restricted, when true, limits Developers to read-only in this
	// environment. Owners are unaffected. Typical use: protect production.
	// +optional
	Restricted bool `json:"restricted,omitempty"`

	// Preview marks this environment as a preview (PR) environment. Set by the
	// preview environment controller; filtered from the normal env list in the UI.
	// +optional
	Preview bool `json:"preview,omitempty"`
}

// ProjectSpec defines the desired state of a Project — the top-level grouping
// above Apps. Each Project owns a control namespace (by default
// `pj-{metadata.name}`) which holds its App CRDs plus one env namespace per
// declared environment (`pj-{metadata.name}-{env}`) which holds the running
// workloads.
type ProjectSpec struct {
	// Description is a short, human-readable note about the project.
	// +optional
	Description string `json:"description,omitempty"`

	// Environments declares the project-level deployment environments. Every
	// App in the project reconciles into every entry here by default. If
	// empty, the controller seeds a single `production` entry.
	// +optional
	Environments []ProjectEnvironment `json:"environments,omitempty"`

	// Preview controls PR-driven preview environments. When Enabled=true,
	// opening a PR clones the source environment with git-source apps
	// pointed at the PR branch. The cloned environment behaves like any
	// other project environment.
	// +optional
	Preview *PreviewConfig `json:"preview,omitempty"`

	// AutoRedeploy controls whether env var changes automatically trigger
	// pod rollouts. When false (default), users must manually redeploy
	// after changing variables. When true, the controller stamps a hash
	// of the env Secret onto the pod template, triggering an automatic
	// rolling update on any change.
	// +optional
	AutoRedeploy bool `json:"autoRedeploy,omitempty"`
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

	// Namespace is the name of the Project's control namespace (`pj-{name}`).
	// Per-env workload namespaces (`pj-{name}-{env}`) are tracked in
	// EnvNamespaces below.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// EnvNamespaces maps environment name → namespace name for each env the
	// controller has provisioned. Kept in sync with spec.environments; env
	// add/remove drives ns create/delete.
	// +optional
	EnvNamespaces map[string]string `json:"envNamespaces,omitempty"`

	// AppCount is the number of Apps currently inside this Project's namespace.
	// +optional
	AppCount int32 `json:"appCount,omitempty"`

	// Environments is the reconciled set of project environment names. Mirrors
	// `spec.environments[].name` after the controller has applied defaulting
	// (e.g. auto-seed `production`) and validation. UI clients should read
	// from `spec.environments` for ordering and from here to confirm the
	// controller has observed a spec change.
	// +optional
	Environments []string `json:"environments,omitempty"`

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
