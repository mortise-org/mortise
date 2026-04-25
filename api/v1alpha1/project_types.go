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

// PreviewConfig is the project-level PR environments toggle (SPEC §5.8).
// When set with Enabled=true, every App inside the Project reconciles into
// each open PR's preview namespace — there is no per-App opt-out.
// Domain, TTL, and Resources act as defaults shared across every PR preview
// spawned from this Project's Apps.
type PreviewConfig struct {
	// Enabled turns PR environments on for every App in the Project.
	Enabled bool `json:"enabled,omitempty"`

	// Domain is a template for preview hostnames. Supports {number} and {app}
	// placeholders (e.g. "pr-{number}-{app}.example.com"). If empty, the
	// controller falls back to a platform-domain-derived default.
	// +optional
	Domain string `json:"domain,omitempty"`

	// TTL is a Go duration string (e.g. "72h") after which an idle preview is
	// garbage-collected. Empty string means "use controller default".
	// +optional
	TTL string `json:"ttl,omitempty"`

	// Resources caps the CPU / memory each PR preview's Pod requests. Empty
	// fields inherit from the staging environment of the source App.
	// +optional
	Resources ResourceRequirements `json:"resources,omitempty"`

	// BotPR, when true, lets PRs opened by bot accounts also spawn previews.
	// Defaults to false — previews only spawn for human-authored PRs.
	// +optional
	BotPR bool `json:"botPR,omitempty"`
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

	// Preview controls PR-driven preview environments at the Project scope
	// (SPEC §5.8). When Enabled=true, every App in this Project participates
	// in each open PR's preview namespace. There is no per-App opt-out in v1.
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
