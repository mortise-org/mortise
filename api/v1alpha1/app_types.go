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

	// ProviderRef is the name of the GitProvider CRD that holds credentials for
	// this repo's forge. Required when type=git.
	// +optional
	ProviderRef string `json:"providerRef,omitempty"`

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

	// Port is the container port the app listens on. Defaults to 8080.
	// The Service always exposes port 80 for HTTP; this controls the
	// targetPort on the Service and containerPort on the Deployment.
	// +optional
	// +kubebuilder:default=8080
	Port int32 `json:"port,omitempty"`
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

// Credential declares a single named credential exposed by this App to any
// binder. Flavor A (spec §5.5a): the Mortise-owned `{app}-credentials` Secret
// is materialised from these entries. At most one of Value / ValueFrom may
// be set; a credential with neither is treated as a well-known key whose
// value the bindings resolver fills in (e.g. `host`, `port`).
type Credential struct {
	// Name is the key written into the {app}-credentials Secret and the env
	// var name projected into binders.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-zA-Z_][a-zA-Z0-9_]*$`
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Value is the inline credential value. Mutually exclusive with ValueFrom.
	// +optional
	Value string `json:"value,omitempty"`

	// ValueFrom references a key in a user-managed Secret in the App's own
	// namespace. Mutually exclusive with Value.
	// +optional
	ValueFrom *CredentialSource `json:"valueFrom,omitempty"`
}

// CredentialSource names an external location the controller should read a
// credential value from. Only SecretRef is supported today.
type CredentialSource struct {
	// SecretRef references a key in a Secret in the App's own namespace.
	// +optional
	SecretRef *SecretKeyRef `json:"secretRef,omitempty"`
}

// SecretKeyRef identifies a single key inside a Secret located in the App's
// own namespace. Cross-namespace references are intentionally not supported
// here — credentials must live beside the App that owns them.
type SecretKeyRef struct {
	// Name of the Secret in the App's namespace.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Key within the Secret.
	// +kubebuilder:validation:Required
	Key string `json:"key"`
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

	// SecretMounts mounts existing k8s Secrets in the App's namespace as
	// files on the container filesystem. See spec §5.5b. Each entry becomes
	// a `Volume` + `VolumeMount` on the Deployment's Pod template. Names
	// must not collide with `spec.storage[].name` — the operator does not
	// reconcile such collisions; the apiserver will reject the Deployment.
	// +optional
	SecretMounts []SecretMount `json:"secretMounts,omitempty"`

	// Annotations are passed through to every k8s resource Mortise creates for
	// this environment (Deployment, pod template, Service, Ingress, PVCs).
	// User-supplied keys win on conflict with Mortise-owned annotations (spec §5.2a).
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// TLS overrides the default cert-manager-driven TLS path for this
	// environment's Ingress (spec §5.6). If nil the operator writes the
	// PlatformConfig default cluster-issuer annotation and auto-generates the
	// Secret name.
	// +optional
	TLS *EnvTLSConfig `json:"tls,omitempty"`
}

// SecretMount mounts an existing k8s Secret in the App's namespace as a
// file-system volume on the container. Secret existence is not validated at
// reconcile time — if the named Secret is missing the Pod stays in
// ContainerCreating until it appears (spec §5.5b: "must exist in the App's
// namespace at Pod-start time").
type SecretMount struct {
	// Name is the Pod volume name. Must be a DNS-1123 label.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// Secret names an existing k8s Secret in the App's namespace. Mortise
	// does not create or manage this Secret.
	// +kubebuilder:validation:Required
	Secret string `json:"secret"`

	// Path is the absolute mount path inside the container. Must begin with
	// a slash.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^/.*`
	Path string `json:"path"`

	// Items projects specific Secret keys to specific filenames (and
	// optional file modes) under Path. When empty, every key in the Secret
	// is surfaced as a file whose name matches the key.
	// +optional
	Items []KeyToPath `json:"items,omitempty"`

	// ReadOnly defaults to true when unset. Pointer so that an explicit
	// `false` is distinguishable from the omitted default.
	// +optional
	ReadOnly *bool `json:"readOnly,omitempty"`
}

// KeyToPath projects one Secret key to a specific filename (and optional
// mode bits) under the mount path.
type KeyToPath struct {
	// Key is the Secret key to project.
	// +kubebuilder:validation:Required
	Key string `json:"key"`

	// Path is the filename (relative to the mount path) to write the key's
	// value to.
	// +kubebuilder:validation:Required
	Path string `json:"path"`

	// Mode is the file mode bits for the projected file (e.g. 0400).
	// +optional
	Mode *int32 `json:"mode,omitempty"`
}

// EnvTLSConfig lets an environment opt out of or reconfigure the bundled
// cert-manager TLS flow. Both fields are optional; when both are unset the
// environment uses the PlatformConfig default.
type EnvTLSConfig struct {
	// SecretName points at a pre-existing k8s Secret in the App's namespace
	// containing tls.crt / tls.key. When set, Mortise skips the cert-manager
	// annotation entirely and wires the Ingress TLS block to this Secret.
	// +optional
	SecretName string `json:"secretName,omitempty"`

	// ClusterIssuer overrides the PlatformConfig default cert-manager
	// ClusterIssuer for this environment. Ignored if SecretName is also set
	// (BYO Secret takes precedence).
	// +optional
	ClusterIssuer string `json:"clusterIssuer,omitempty"`
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

	// Credentials declares the keys this App exposes to binders. The App
	// controller materialises the `{app}-credentials` Secret from these
	// entries; the bindings resolver projects them into binder Pods (spec §5.5a).
	// +optional
	Credentials []Credential `json:"credentials,omitempty"`

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

	// LastBuiltSHA is the git commit SHA of the most recently completed build.
	// The reconciler uses this to short-circuit rebuilds when the revision hasn't changed.
	// +optional
	LastBuiltSHA string `json:"lastBuiltSHA,omitempty"`

	// LastBuiltImage is the full image reference (including digest) produced by the
	// most recently completed build. The Deployment spec is set to this value.
	// +optional
	LastBuiltImage string `json:"lastBuiltImage,omitempty"`

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
