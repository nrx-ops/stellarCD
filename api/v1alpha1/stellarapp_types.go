/*
Copyright 2026 nrx-ops.

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

// Phase is a coarse, human-readable summary of where a StellarApp sits in the
// reconciliation workflow. Conditions carry the machine-readable detail.
type Phase string

const (
	PhaseUnknown  Phase = "Unknown"
	PhaseSyncing  Phase = "Syncing"
	PhaseSynced   Phase = "Synced"
	PhaseApplying Phase = "Applying"
	PhaseDegraded Phase = "Degraded"
)

// Condition types maintained on StellarAppStatus.
const (
	// ConditionReady is true once the desired state has been applied and the
	// infrastructure is believed to match the tracked Git revision.
	ConditionReady = "Ready"
	// ConditionSynced is true once the Git repository has been fetched and the
	// tracked revision resolved.
	ConditionSynced = "Synced"
)

// ExecutorType selects the binary used to apply infrastructure changes.
type ExecutorType string

const (
	ExecutorTypeTerraform  ExecutorType = "Terraform"
	ExecutorTypeTerragrunt ExecutorType = "Terragrunt"
)

// GitRepositorySpec defines Git repository configuration.
type GitRepositorySpec struct {
	// URL is the Git repository URL.
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`
	// Ref is the Git branch or tag to track.
	// +kubebuilder:default="main"
	// +kubebuilder:validation:MinLength=1
	// +optional
	Ref string `json:"ref,omitempty"`
	// SecretRef references a Secret holding the Git authentication credentials.
	// +optional
	SecretRef *SecretRef `json:"secretRef,omitempty"`
}

// SecretRef references a Kubernetes Secret in the StellarApp's own namespace.
type SecretRef struct {
	// Name is the name of the referenced Secret.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// StellarAppSpec defines the desired state of StellarApp.
type StellarAppSpec struct {
	// GitRepository contains the Git repository configuration.
	GitRepository GitRepositorySpec `json:"gitRepository"`
	// TerraformPath is the path to the Terraform/Terragrunt directory in the Git repository.
	// +kubebuilder:validation:MinLength=1
	TerraformPath string `json:"terraformPath"`
	// Interval is the reconciliation interval, e.g. "5m".
	// +kubebuilder:default="5m"
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`
	// Executor is the tool used to apply infrastructure changes.
	// +kubebuilder:validation:Enum=Terraform;Terragrunt
	// +kubebuilder:default=Terraform
	// +optional
	Executor ExecutorType `json:"executor,omitempty"`
	// AutoApply enables automatic application of Terraform plans. When false the
	// controller stops after producing a plan.
	// +kubebuilder:default=false
	// +optional
	AutoApply bool `json:"autoApply,omitempty"`
}

// StellarAppStatus defines the observed state of StellarApp.
type StellarAppStatus struct {
	// Phase is a coarse summary of the current reconciliation state.
	// +optional
	Phase Phase `json:"phase,omitempty"`
	// Conditions holds the machine-readable state of the StellarApp.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// LastSyncTime is the timestamp of the last successful sync.
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`
	// LastError contains the last error message, cleared on a successful sync.
	// +optional
	LastError string `json:"lastError,omitempty"`
	// ObservedGeneration reflects the generation of the spec that was last processed.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// ReconcileInterval returns the configured reconciliation interval, falling back
// to fallback when the spec leaves it unset. The CRD default covers objects
// created through the API server; the fallback covers objects built in tests or
// decoded from manifests that predate the default.
func (s *StellarAppSpec) ReconcileInterval(fallback metav1.Duration) metav1.Duration {
	if s.Interval == nil || s.Interval.Duration <= 0 {
		return fallback
	}
	return *s.Interval
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=sapp;sapps
// +kubebuilder:printcolumn:name="Git Repo",type=string,JSONPath=`.spec.gitRepository.url`
// +kubebuilder:printcolumn:name="Path",type=string,JSONPath=`.spec.terraformPath`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// StellarApp is the Schema for the stellarapps API.
type StellarApp struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   StellarAppSpec   `json:"spec,omitempty"`
	Status StellarAppStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// StellarAppList contains a list of StellarApp.
type StellarAppList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []StellarApp `json:"items"`
}

func init() {
	SchemeBuilder.Register(&StellarApp{}, &StellarAppList{})
}
