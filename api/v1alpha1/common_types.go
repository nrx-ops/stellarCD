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

// Engine is the binary that actually runs init, plan and apply, and owns the
// state. Terragrunt is deliberately not a value here: it is a wrapper that
// drives one of these engines, so it is configured on its own in
// TerragruntSpec and combines with either engine.
// +kubebuilder:validation:Enum=Terraform;OpenTofu
type Engine string

const (
	EngineTerraform Engine = "Terraform"
	EngineOpenTofu  Engine = "OpenTofu"
)

// TerragruntMode decides what an Astral's path points at when Terragrunt is on.
// +kubebuilder:validation:Enum=Unit;RunAll
type TerragruntMode string

const (
	// TerragruntModeUnit means the path is a single unit, so a single state.
	TerragruntModeUnit TerragruntMode = "Unit"
	// TerragruntModeRunAll means the path is a parent directory: every unit
	// below it runs, each with its own state, and status is tracked per unit.
	TerragruntModeRunAll TerragruntMode = "RunAll"
)

// TerragruntSpec puts Terragrunt in front of the engine.
type TerragruntSpec struct {
	// Enabled routes every command through Terragrunt.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// Version is the exact Terragrunt version, e.g. "1.1.6". Empty means the
	// version the runner image ships; a mismatch fails the Flare before init.
	// +kubebuilder:validation:Pattern=`^$|^v?[0-9]+\.[0-9]+(\.[0-9]+)?$`
	// +optional
	Version string `json:"version,omitempty"`
	// Mode selects a single unit or every unit below the path.
	// +kubebuilder:default=Unit
	// +optional
	Mode TerragruntMode `json:"mode,omitempty"`
	// ExcludedUnits are skipped by whole-Astral runs in RunAll mode, typically
	// a unit that is broken and being fixed. Paths are relative to the Astral
	// path. They are never inherited.
	// +optional
	ExcludedUnits []string `json:"excludedUnits,omitempty"`
}

// RunAll reports whether the spec runs every unit below the path.
func (t *TerragruntSpec) RunAll() bool {
	return t != nil && t.Enabled && t.Mode == TerragruntModeRunAll
}

// SyncStatus says whether real infrastructure matches the tracked revision.
// +kubebuilder:validation:Enum=Synced;OutOfSync;Unknown
type SyncStatus string

const (
	// SyncStatusSynced means the last plan found nothing to change.
	SyncStatusSynced SyncStatus = "Synced"
	// SyncStatusOutOfSync means the last plan found changes to apply.
	SyncStatusOutOfSync SyncStatus = "OutOfSync"
	// SyncStatusUnknown means no plan has succeeded yet.
	SyncStatusUnknown SyncStatus = "Unknown"
)

// Worse returns the worse of two sync statuses, used to fold per-unit results
// into one Astral status: one drifted unit makes the whole Astral drifted.
func (s SyncStatus) Worse(other SyncStatus) SyncStatus {
	rank := func(v SyncStatus) int {
		switch v {
		case SyncStatusOutOfSync:
			return 2
		case SyncStatusUnknown:
			return 1
		default:
			return 0
		}
	}
	if rank(other) > rank(s) {
		return other
	}
	return s
}

// PlanSummary counts what a plan would change. A replacement deletes the
// resource before recreating it, so it is counted on its own and also treated
// as a destruction by the destroy protection.
type PlanSummary struct {
	// ToCreate is the number of resources the plan adds.
	// +optional
	ToCreate int32 `json:"toCreate"`
	// ToUpdate is the number of resources changed in place.
	// +optional
	ToUpdate int32 `json:"toUpdate"`
	// ToDestroy is the number of resources the plan deletes outright.
	// +optional
	ToDestroy int32 `json:"toDestroy"`
	// ToReplace is the number of resources deleted and recreated.
	// +optional
	ToReplace int32 `json:"toReplace"`
}

// HasChanges reports whether the plan would change anything.
func (p *PlanSummary) HasChanges() bool {
	return p != nil && (p.ToCreate > 0 || p.ToUpdate > 0 || p.ToDestroy > 0 || p.ToReplace > 0)
}

// Destroys reports how many resources the plan deletes, replacements included.
func (p *PlanSummary) Destroys() int32 {
	if p == nil {
		return 0
	}
	return p.ToDestroy + p.ToReplace
}

// Add folds another summary into this one.
func (p *PlanSummary) Add(other *PlanSummary) {
	if other == nil {
		return
	}
	p.ToCreate += other.ToCreate
	p.ToUpdate += other.ToUpdate
	p.ToDestroy += other.ToDestroy
	p.ToReplace += other.ToReplace
}

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
	// variable name. Terraform reads inputs from TF_VAR_<name>, hence the
	// default. Set it to "" for cloud credentials, which providers read under
	// their own names (AWS_ACCESS_KEY_ID, GOOGLE_CREDENTIALS, ...). It is a
	// pointer so that "" can be told apart from "not set".
	// +kubebuilder:default="TF_VAR_"
	// +optional
	EnvPrefix *string `json:"envPrefix,omitempty"`
}

// DefaultEnvPrefix is the prefix used when envPrefix is not set.
const DefaultEnvPrefix = "TF_VAR_"

// Prefix returns the effective env var prefix.
func (r EnvSecretRef) Prefix() string {
	if r.EnvPrefix == nil {
		return DefaultEnvPrefix
	}
	return *r.EnvPrefix
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
