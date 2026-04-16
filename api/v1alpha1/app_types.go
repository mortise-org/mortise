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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SourceType determines how an App is deployed.
// +kubebuilder:validation:Enum=git;image
type SourceType string

const (
	SourceTypeGit   SourceType = "git"
	SourceTypeImage SourceType = "image"
)

// BuildMode determines how source is built.
// +kubebuilder:validation:Enum=auto;dockerfile;railpack
type BuildMode string

const (
	BuildModeAuto       BuildMode = "auto"
	BuildModeDockerfile BuildMode = "dockerfile"
	BuildModeRailpack   BuildMode = "railpack"
)

type AppSource struct {
	// +kubebuilder:validation:Required
	Type SourceType `json:"type"`

	// Git source fields (used when type=git)
	Repo       string   `json:"repo,omitempty"`
	Branch     string   `json:"branch,omitempty"`
	Path       string   `json:"path,omitempty"`
	WatchPaths []string `json:"watchPaths,omitempty"`
	Build      *Build   `json:"build,omitempty"`

	// Image source fields (used when type=image)
	Image         string `json:"image,omitempty"`
	PullSecretRef string `json:"pullSecretRef,omitempty"`
}

type Build struct {
	Mode           BuildMode         `json:"mode,omitempty"`
	DockerfilePath string            `json:"dockerfilePath,omitempty"`
	Cache          *bool             `json:"cache,omitempty"`
	Args           map[string]string `json:"args,omitempty"`
}

type NetworkConfig struct {
	// +kubebuilder:default=true
	Public bool `json:"public,omitempty"`
}

type VolumeSpec struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	MountPath    string            `json:"mountPath"`
	Size         resource.Quantity `json:"size,omitempty"`
	StorageClass string            `json:"storageClass,omitempty"`
	AccessMode   string            `json:"accessMode,omitempty"`
}

type EnvVar struct {
	Name      string        `json:"name"`
	Value     string        `json:"value,omitempty"`
	ValueFrom *EnvVarSource `json:"valueFrom,omitempty"`
}

type EnvVarSource struct {
	SecretRef string `json:"secretRef,omitempty"`
}

type Binding struct {
	// Ref is the name of the bound App. By default the ref is resolved within
	// the binder's own project namespace.
	Ref string `json:"ref"`

	// Project, if set, resolves the ref in the namespace of the named Project
	// (`project-{project}`) instead of the binder's own namespace. Enables
	// cross-project bindings (e.g. binding app in `web` project to a db in
	// `infra` project).
	// +optional
	Project string `json:"project,omitempty"`
}

type ResourceRequirements struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

type Environment struct {
	// +kubebuilder:validation:Required
	Name          string               `json:"name"`
	Replicas      *int32               `json:"replicas,omitempty"`
	Resources     ResourceRequirements `json:"resources,omitempty"`
	Env           []EnvVar             `json:"env,omitempty"`
	Bindings      []Binding            `json:"bindings,omitempty"`
	Domain        string               `json:"domain,omitempty"`
	CustomDomains []string             `json:"customDomains,omitempty"`
}

type PreviewConfig struct {
	Enabled   bool                 `json:"enabled,omitempty"`
	Domain    string               `json:"domain,omitempty"`
	TTL       string               `json:"ttl,omitempty"`
	Resources ResourceRequirements `json:"resources,omitempty"`
}

// AppSpec defines the desired state of App
type AppSpec struct {
	// +kubebuilder:validation:Required
	Source AppSource `json:"source"`

	Network NetworkConfig `json:"network,omitempty"`

	Storage []VolumeSpec `json:"storage,omitempty"`

	Credentials []string `json:"credentials,omitempty"`

	Environments []Environment `json:"environments,omitempty"`

	Preview *PreviewConfig `json:"preview,omitempty"`
}

// DeployRecord tracks a single deployment for rollback.
type DeployRecord struct {
	Image     string      `json:"image"`
	Digest    string      `json:"digest,omitempty"`
	GitSHA    string      `json:"gitSHA,omitempty"`
	Timestamp metav1.Time `json:"timestamp"`
}

// EnvironmentStatus tracks the observed state of a single environment.
type EnvironmentStatus struct {
	Name          string         `json:"name"`
	ReadyReplicas int32          `json:"readyReplicas,omitempty"`
	CurrentImage  string         `json:"currentImage,omitempty"`
	CurrentDigest string         `json:"currentDigest,omitempty"`
	DeployHistory []DeployRecord `json:"deployHistory,omitempty"`
}

// AppPhase represents the overall lifecycle phase.
// +kubebuilder:validation:Enum=Pending;Building;Deploying;Ready;Failed
type AppPhase string

const (
	AppPhasePending   AppPhase = "Pending"
	AppPhaseBuilding  AppPhase = "Building"
	AppPhaseDeploying AppPhase = "Deploying"
	AppPhaseReady     AppPhase = "Ready"
	AppPhaseFailed    AppPhase = "Failed"
)

// AppStatus defines the observed state of App.
type AppStatus struct {
	Phase        AppPhase            `json:"phase,omitempty"`
	Environments []EnvironmentStatus `json:"environments,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.source.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// App is the Schema for the apps API
type App struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec AppSpec `json:"spec"`

	// +optional
	Status AppStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AppList contains a list of App
type AppList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []App `json:"items"`
}

func init() {
	SchemeBuilder.Register(&App{}, &AppList{})
}
