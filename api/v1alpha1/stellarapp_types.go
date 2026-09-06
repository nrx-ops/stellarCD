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

type Phase string

const (
	PhaseUnknown  Phase = "Unknown"
	PhaseSyncing  Phase = "Syncing"
	PhaseSynced   Phase = "Synced"
	PhaseApplying Phase = "Applying"
	PhaseDegraded Phase = "Degraded"
)

type ExecutorType string

const (
	ExecutorTypeTerraform  ExecutorType = "Terraform"
	ExecutorTypeTerragrunt ExecutorType = "Terragrunt"
)

// GitRepositorySpec defines Git repository configuration
type GitRepositorySpec struct {
	// URL is the Git repository URL
	URL string `json:"url"`
	// Ref is the Git branch/tag to track (default: main)
	Ref string `json:"ref,omitempty"`
	// SecretRef is a reference to a Secret containing authentication credentials
	SecretRef *SecretRef `json:"secretRef,omitempty"`
}

// SecretRef references a Kubernetes Secret
type SecretRef struct {
	Name string `json:"name"`
}

// StellarAppSpec defines the desired state of StellarApp
type StellarAppSpec struct {
	// GitRepository contains Git repository configuration
	GitRepository GitRepositorySpec `json:"gitRepository"`
	// TerraformPath is the path to the Terraform/Terragrunt directory in the Git repo
	TerraformPath string `json:"terraformPath"`
	// Interval is the reconciliation interval (e.g., "5m")
	Interval string `json:"interval,omitempty"`
	// Executor is the tool to use for infrastructure changes
	// +kubebuilder:validation:Enum=Terraform;Terragrunt
	Executor ExecutorType `json:"executor,omitempty"`
	// AutoApply enables automatic application of terraform plans
	AutoApply bool `json:"autoApply,omitempty"`
}

// StellarAppStatus defines the observed state of StellarApp
type StellarAppStatus struct {
	// Phase indicates the current phase of reconciliation
	Phase Phase `json:"phase,omitempty"`
	// LastSyncTime is the timestamp of the last successful sync
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`
	// LastError contains the last error message
	LastError string `json:"lastError,omitempty"`
	// ObservedGeneration reflects the generation of the spec that was last processed
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=sapp;stapps
// +kubebuilder:printcolumn:name="Git Repo",type=string,JSONPath=`.spec.gitRepository.url`
// +kubebuilder:printcolumn:name="Terraform Path",type=string,JSONPath=`.spec.terraformPath`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// StellarApp is the Schema for the stellarapps API
type StellarApp struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   StellarAppSpec   `json:"spec,omitempty"`
	Status StellarAppStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// StellarAppList contains a list of StellarApp
type StellarAppList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []StellarApp `json:"items"`
}
