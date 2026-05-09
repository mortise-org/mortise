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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BuildRunPhase is the durable lifecycle state of a git build execution.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
type BuildRunPhase string

const (
	BuildRunPhasePending   BuildRunPhase = "Pending"
	BuildRunPhaseRunning   BuildRunPhase = "Running"
	BuildRunPhaseSucceeded BuildRunPhase = "Succeeded"
	BuildRunPhaseFailed    BuildRunPhase = "Failed"
)

const (
	BuildRunTargetAppEnvironment     = "AppEnvironment"
	BuildRunTargetPreviewEnvironment = "PreviewEnvironment"
)

// BuildRunTargetRef identifies the resource requesting the build.
type BuildRunTargetRef struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// BuildRunTrigger describes why a BuildRun exists.
type BuildRunTrigger string

const (
	BuildRunTriggerAuto    BuildRunTrigger = "auto"
	BuildRunTriggerManual  BuildRunTrigger = "manual"
	BuildRunTriggerPreview BuildRunTrigger = "preview"
	BuildRunTriggerWebhook BuildRunTrigger = "webhook"
)

// BuildRunReference points at a durable build execution.
type BuildRunReference struct {
	Name  string        `json:"name"`
	Phase BuildRunPhase `json:"phase,omitempty"`
}

// BuildRunLogReference points at persisted build logs.
type BuildRunLogReference struct {
	Kind string `json:"kind,omitempty"`
	Name string `json:"name,omitempty"`
}

// BuildRunSpec defines the desired state of BuildRun.
type BuildRunSpec struct {
	AppName        string            `json:"appName,omitempty"`
	TargetRef      BuildRunTargetRef `json:"targetRef"`
	Environment    string            `json:"environment,omitempty"`
	Trigger        BuildRunTrigger   `json:"trigger,omitempty"`
	RequestID      string            `json:"requestID,omitempty"`
	ProviderRef    string            `json:"providerRef,omitempty"`
	CreatedBy      string            `json:"createdBy,omitempty"`
	TokenOwner     string            `json:"tokenOwner,omitempty"`
	Repo           string            `json:"repo"`
	Branch         string            `json:"branch,omitempty"`
	Revision       string            `json:"revision,omitempty"`
	SourcePath     string            `json:"sourcePath,omitempty"`
	Path           string            `json:"path,omitempty"`
	BuildMode      string            `json:"buildMode,omitempty"`
	DockerfilePath string            `json:"dockerfilePath,omitempty"`
	Dockerfile     string            `json:"dockerfile,omitempty"`
	BuildContext   BuildContext      `json:"buildContext,omitempty"`
	BuildArgs      map[string]string `json:"buildArgs,omitempty"`
	PushTarget     string            `json:"pushTarget,omitempty"`
	PushImage      string            `json:"pushImage,omitempty"`
	PullTarget     string            `json:"pullTarget,omitempty"`
	PullImage      string            `json:"pullImage,omitempty"`
	NoCache        bool              `json:"noCache,omitempty"`
	InputHash      string            `json:"inputHash,omitempty"`
	TokenSecretRef *SecretRef        `json:"tokenSecretRef,omitempty"`
}

// BuildRunStatus defines the observed state of BuildRun.
type BuildRunStatus struct {
	Phase          BuildRunPhase                `json:"phase,omitempty"`
	JobRef         *corev1.LocalObjectReference `json:"jobRef,omitempty"`
	LogRef         *corev1.LocalObjectReference `json:"logRef,omitempty"`
	Image          string                       `json:"image,omitempty"`
	Digest         string                       `json:"digest,omitempty"`
	DetectedPort   int32                        `json:"detectedPort,omitempty"`
	StartedAt      *metav1.Time                 `json:"startedAt,omitempty"`
	CompletedAt    *metav1.Time                 `json:"completedAt,omitempty"`
	FinishedAt     *metav1.Time                 `json:"finishedAt,omitempty"`
	Error          string                       `json:"error,omitempty"`
	FailureReason  string                       `json:"failureReason,omitempty"`
	FailureMessage string                       `json:"failureMessage,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetRef.name`
// +kubebuilder:printcolumn:name="Env",type=string,JSONPath=`.spec.environment`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Revision",type=string,JSONPath=`.spec.revision`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BuildRun is the Schema for durable build executions.
type BuildRun struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec BuildRunSpec `json:"spec"`
	// +optional
	Status BuildRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BuildRunList contains a list of BuildRun.
type BuildRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BuildRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BuildRun{}, &BuildRunList{})
}
