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
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Condition types shared by the Universe -> Galaxy -> Astral -> Flare chain.
// Each object also keeps kind-specific conditions declared next to its type.
const (
	// ConditionReconciled is True once the controller has finished a full pass
	// over the current spec generation without an error.
	ConditionReconciled = "Reconciled"
	// ConditionParentResolved is True once every *Ref in the spec points at an
	// existing, non-suspended ancestor.
	ConditionParentResolved = "ParentResolved"
	// ConditionDiscovered reports the outcome of the last repository walk. It
	// is absent on a Galaxy that does not use discovery.
	ConditionDiscovered = "Discovered"
)

// VCSProvider identifies the hosting platform backing a repository. It drives
// webhook payload parsing and the credential layout expected in the Secret.
type VCSProvider string

const (
	VCSProviderGitHub VCSProvider = "GitHub"
	VCSProviderGitLab VCSProvider = "GitLab"
	VCSProviderGitea  VCSProvider = "Gitea"
)

// TerrariumEngine is the infrastructure-as-code binary used to reach the
// desired state. "Terrarium" is the stellarCD umbrella term for the family.
type TerrariumEngine string

const (
	TerrariumEngineTerraform  TerrariumEngine = "Terraform"
	TerrariumEngineOpenTofu   TerrariumEngine = "OpenTofu"
	TerrariumEngineTerragrunt TerrariumEngine = "Terragrunt"
)

// ApprovalPolicy decides whether a Flare may leave the Pending phase on its own.
type ApprovalPolicy string

const (
	// ApprovalPolicyAuto lets any Flare run as soon as it is admitted.
	ApprovalPolicyAuto ApprovalPolicy = "Auto"
	// ApprovalPolicyManual holds every Flare until spec.approved is set to true.
	ApprovalPolicyManual ApprovalPolicy = "Manual"
	// ApprovalPolicyRequiresReview holds mutating Flares (Apply, Destroy) but
	// lets read-only Flares (Plan, Refresh) run unattended.
	ApprovalPolicyRequiresReview ApprovalPolicy = "RequiresReview"
)

// BackendType selects the remote state backend provisioned for a Galaxy.
type BackendType string

const (
	BackendTypeS3        BackendType = "S3"
	BackendTypeGCS       BackendType = "GCS"
	BackendTypeAzureBlob BackendType = "AzureBlob"
)

// TerrariumSpec pins the engine and version used across a Galaxy. Astrals may
// override the version but not the engine, so a Galaxy stays homogeneous.
type TerrariumSpec struct {
	// Engine is the binary invoked to plan and apply infrastructure.
	// +kubebuilder:validation:Enum=Terraform;OpenTofu;Terragrunt
	// +kubebuilder:default=Terraform
	// +optional
	Engine TerrariumEngine `json:"engine,omitempty"`
	// Version is the exact engine version, e.g. "1.9.5". An empty value means
	// "whatever version the runner image ships".
	// +kubebuilder:validation:Pattern=`^$|^v?[0-9]+\.[0-9]+(\.[0-9]+)?$`
	// +optional
	Version string `json:"version,omitempty"`
}

// BackendConfig describes the remote state backend. Only the fields relevant to
// the selected type are honoured; the rest are ignored by the controller.
type BackendConfig struct {
	// Type selects the backend implementation.
	// +kubebuilder:validation:Enum=S3;GCS;AzureBlob
	Type BackendType `json:"type"`
	// Bucket is the S3 bucket or GCS bucket holding the state files.
	// +optional
	Bucket string `json:"bucket,omitempty"`
	// Container is the Azure Blob container holding the state files.
	// +optional
	Container string `json:"container,omitempty"`
	// StorageAccount is the Azure Blob storage account.
	// +optional
	StorageAccount string `json:"storageAccount,omitempty"`
	// Prefix is prepended to every state key, letting one bucket host many
	// Galaxies without collisions.
	// +optional
	Prefix string `json:"prefix,omitempty"`
	// Region is the cloud region hosting the backend.
	// +optional
	Region string `json:"region,omitempty"`
	// SecretRef references the Secret holding the backend credentials. Its keys
	// are injected as environment variables into the executor process.
	// +optional
	SecretRef *SecretRef `json:"secretRef,omitempty"`
}

// RBACConfig maps identities onto the tenant Roles created inside a Universe
// namespace. Entries are Kubernetes user names as seen by the authenticator.
type RBACConfig struct {
	// AdminUsers may create, mutate and delete every stellarCD object inside
	// the Universe namespace.
	// +optional
	AdminUsers []string `json:"adminUsers,omitempty"`
	// ViewerUsers hold read-only access to the Universe namespace.
	// +optional
	ViewerUsers []string `json:"viewerUsers,omitempty"`
}

// HasBindings reports whether any identity needs a RoleBinding.
func (r *RBACConfig) HasBindings() bool {
	return r != nil && (len(r.AdminUsers) > 0 || len(r.ViewerUsers) > 0)
}

// UniverseQuota caps the aggregate compute a Universe namespace may consume.
// It is translated verbatim into a Kubernetes ResourceQuota.
type UniverseQuota struct {
	// CPU is the total CPU limit, e.g. "8".
	// +optional
	CPU *resource.Quantity `json:"cpu,omitempty"`
	// Memory is the total memory limit, e.g. "16Gi".
	// +optional
	Memory *resource.Quantity `json:"memory,omitempty"`
	// Storage is the total requested storage, e.g. "100Gi".
	// +optional
	Storage *resource.Quantity `json:"storage,omitempty"`
}

// IsEmpty reports whether the quota constrains nothing, in which case the
// controller skips creating a ResourceQuota object.
func (q *UniverseQuota) IsEmpty() bool {
	return q == nil || (q.CPU == nil && q.Memory == nil && q.Storage == nil)
}

// UniverseNetworkPolicy is a named NetworkPolicy materialised inside the
// Universe namespace. The spec is passed through untouched so operators keep
// the full expressiveness of the upstream API.
type UniverseNetworkPolicy struct {
	// Name is the NetworkPolicy object name inside the Universe namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// Spec is the upstream networking.k8s.io/v1 NetworkPolicySpec.
	Spec networkingv1.NetworkPolicySpec `json:"spec"`
}

// VCSRepository locates the infrastructure code for an Astral.
type VCSRepository struct {
	// Provider identifies the hosting platform.
	// +kubebuilder:validation:Enum=GitHub;GitLab;Gitea
	// +kubebuilder:default=GitHub
	// +optional
	Provider VCSProvider `json:"provider,omitempty"`
	// URL is the clone URL, HTTPS or SSH.
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`
	// Branch is the tracked branch.
	// +kubebuilder:default="main"
	// +kubebuilder:validation:MinLength=1
	// +optional
	Branch string `json:"branch,omitempty"`
	// Path is the directory inside the repository holding the root module.
	// Relative to the repository root; "." means the root itself.
	// +kubebuilder:default="."
	// +optional
	Path string `json:"path,omitempty"`
	// SecretRef references the Secret holding the clone credentials. When unset
	// the Galaxy-level credentials are used.
	// +optional
	SecretRef *SecretRef `json:"secretRef,omitempty"`
}

// EnvSecretRef projects a Secret into the executor environment. It is the only
// supported way to pass sensitive Terraform inputs: values never transit
// through the spec, the status or the logs.
type EnvSecretRef struct {
	// Name is the Secret name, resolved in the object's own namespace.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Key selects a single entry from the Secret. When empty every key in the
	// Secret is projected.
	// +optional
	Key string `json:"key,omitempty"`
	// EnvPrefix is prepended to each projected key to build the environment
	// variable name. Terraform reads inputs from TF_VAR_<name>.
	// +kubebuilder:default="TF_VAR_"
	// +optional
	EnvPrefix string `json:"envPrefix,omitempty"`
}

// ObjectRefStatus is a lightweight back-reference recorded on a status. It is
// deliberately not a corev1.ObjectReference: the extra fields of that type are
// unused here and confuse consumers about what the controller actually sets.
type ObjectRefStatus struct {
	// Name is the referenced object's name.
	Name string `json:"name"`
	// Namespace is the referenced object's namespace, empty for cluster scope.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// Hierarchy labels. Controllers stamp them on every object they create so a
// whole tenant can be listed with a single label selector instead of a
// full-namespace scan plus client-side filtering.
const (
	// LabelUniverse carries the owning Universe name.
	LabelUniverse = "core.stellarcd.io/universe"
	// LabelGalaxy carries the owning Galaxy name.
	LabelGalaxy = "core.stellarcd.io/galaxy"
	// LabelAstral carries the owning Astral name.
	LabelAstral = "core.stellarcd.io/astral"
	// LabelManagedBy marks objects whose contents stellarCD owns and overwrites.
	LabelManagedBy = "app.kubernetes.io/managed-by"
	// LabelDiscoveredBy marks an Astral that repository discovery created, and
	// names the Galaxy that owns it. Pruning is restricted to objects carrying
	// it, so a hand-written Astral is never deleted by the routine.
	LabelDiscoveredBy = "core.stellarcd.io/discovered-by"
	// AnnotationDiscoveredPath records the module directory an Astral came
	// from, so a renamed module is recognised as the same one moving rather
	// than as a delete plus a create.
	AnnotationDiscoveredPath = "core.stellarcd.io/discovered-path"
	// ManagedByValue is the value written to LabelManagedBy.
	ManagedByValue = "stellarcd"
)
