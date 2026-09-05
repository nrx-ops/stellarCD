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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ExecutorType selects the IaC tool used to reconcile a StellarApp.
// +kubebuilder:validation:Enum=terraform;terragrunt
type ExecutorType string

const (
	ExecutorTerraform  ExecutorType = "terraform"
	ExecutorTerragrunt ExecutorType = "terragrunt"
)

// Phase represents the high-level reconciliation state of a StellarApp.
type Phase string

const (
	// PhaseSyncing means the controller is fetching the Git repository.
	PhaseSyncing Phase = "Syncing"
	// PhaseApplying means a Terraform/Terragrunt apply is in progress.
	PhaseApplying Phase = "Applying"
	// PhaseSynced means the last reconciliation succeeded and the infrastructure matches the desired state.
	PhaseSynced Phase = "Synced"
	// PhaseDegraded means the last reconciliation failed.
	PhaseDegraded Phase = "Degraded"
)

// GitRepositorySpec describes the Git source to reconcile.
type GitRepositorySpec struct {
	// url is the Git repository URL (HTTPS or SSH).
	// +required
	URL string `json:"url"`

	// revision is the Git branch, tag, or commit SHA to check out.
	// +kubebuilder:default=main
	// +optional
	Revision string `json:"revision,omitempty"`

	// credentialsSecretRef references a Secret holding the Git SSH key or access token used to fetch the
	// repository. The secret is read at runtime and never logged.
	// +optional
	CredentialsSecretRef *corev1.LocalObjectReference `json:"credentialsSecretRef,omitempty"`
}

// StellarAppSpec defines the desired state of StellarApp
type StellarAppSpec struct {
	// gitRepository is the source Git repository containing the Terraform/Terragrunt configuration.
	// +required
	GitRepository GitRepositorySpec `json:"gitRepository"`

	// path is the directory within the repository containing the Terraform/Terragrunt root module to reconcile.
	// +kubebuilder:default=""
	// +optional
	Path string `json:"path,omitempty"`

	// executor selects the IaC tool used to reconcile this application.
	// +kubebuilder:default=terraform
	// +optional
	Executor ExecutorType `json:"executor,omitempty"`

	// interval is the reconciliation interval at which the controller polls the Git repository and
	// corrects drift.
	// +kubebuilder:default="5m"
	// +optional
	Interval metav1.Duration `json:"interval,omitempty"`

	// cloudCredentialsSecretRef references a Secret holding cloud provider credentials. Its keys are
	// injected as environment variables into the Terraform/Terragrunt execution environment.
	// +optional
	CloudCredentialsSecretRef *corev1.LocalObjectReference `json:"cloudCredentialsSecretRef,omitempty"`
}

// StellarAppStatus defines the observed state of StellarApp.
type StellarAppStatus struct {
	// phase is the high-level reconciliation state of the StellarApp.
	// +optional
	Phase Phase `json:"phase,omitempty"`

	// lastSyncedRevision is the Git commit SHA that was last successfully applied.
	// +optional
	LastSyncedRevision string `json:"lastSyncedRevision,omitempty"`

	// lastSyncTime is the timestamp of the last successful reconciliation.
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// observedGeneration reflects the generation most recently observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions represent the current state of the StellarApp resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Revision",type=string,JSONPath=`.status.lastSyncedRevision`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// StellarApp is the Schema for the stellarapps API
type StellarApp struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of StellarApp
	// +required
	Spec StellarAppSpec `json:"spec"`

	// status defines the observed state of StellarApp
	// +optional
	Status StellarAppStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// StellarAppList contains a list of StellarApp
type StellarAppList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []StellarApp `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &StellarApp{}, &StellarAppList{})
		return nil
	})
}
