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

// UniverseFinalizer guards the tenant namespace so it is torn down only after
// every Galaxy inside it has been reclaimed.
const UniverseFinalizer = "core.stellarcd.io/universe-cleanup"

// NamespacePrefix is prepended to a Universe name to derive its isolation
// namespace. Deriving rather than accepting a free-form namespace keeps two
// Universes from ever sharing one.
const NamespacePrefix = "stellar-"

// UniversePhase summarises a Universe at a glance. Conditions carry the detail.
type UniversePhase string

const (
	// UniversePhasePending means the isolation namespace is not ready yet.
	UniversePhasePending UniversePhase = "Pending"
	// UniversePhaseActive means the namespace, quota, policies and RBAC are in place.
	UniversePhaseActive UniversePhase = "Active"
	// UniversePhaseSuspended means spec.suspended is set: existing objects stay
	// but no Flare inside the Universe is allowed to run.
	UniversePhaseSuspended UniversePhase = "Suspended"
	// UniversePhaseDeleting means the finalizer is draining the tenant.
	UniversePhaseDeleting UniversePhase = "Deleting"
)

// Universe-specific condition types.
const (
	// ConditionNamespaceReady is True once the tenant namespace exists.
	ConditionNamespaceReady = "NamespaceReady"
	// ConditionQuotaEnforced is True once the ResourceQuota matches spec.defaultQuota.
	ConditionQuotaEnforced = "QuotaEnforced"
	// ConditionPoliciesApplied is True once every NetworkPolicy and RoleBinding is in sync.
	ConditionPoliciesApplied = "PoliciesApplied"
)

// UniverseSpec defines the desired state of a Universe.
//
// A Universe is the outermost container of the stellarCD hierarchy: it owns one
// Kubernetes namespace and every Galaxy, Astral and Flare lives inside it.
// Identity comes from metadata.name, which is already unique cluster-wide;
// spec.displayName only carries the human-facing label.
type UniverseSpec struct {
	// DisplayName is the human-readable tenant name shown in the dashboard.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	DisplayName string `json:"displayName,omitempty"`
	// Description explains what this tenant is for.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Description string `json:"description,omitempty"`
	// DefaultQuota caps the aggregate resources the tenant namespace may use.
	// Omitting it leaves the namespace unbounded.
	// +optional
	DefaultQuota *UniverseQuota `json:"defaultQuota,omitempty"`
	// Labels are propagated onto the tenant namespace.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// Annotations are propagated onto the tenant namespace.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
	// NetworkPolicies are reconciled into the tenant namespace. stellarCD owns
	// these objects: manual edits are reverted on the next pass.
	// +listType=map
	// +listMapKey=name
	// +optional
	NetworkPolicies []UniverseNetworkPolicy `json:"networkPolicies,omitempty"`
	// RBAC grants namespace-scoped access to the listed identities.
	// +optional
	RBAC *RBACConfig `json:"rbac,omitempty"`
	// Suspended freezes the tenant: Flares stay Pending instead of running.
	// Existing objects are left untouched.
	// +kubebuilder:default=false
	// +optional
	Suspended bool `json:"suspended,omitempty"`
}

// UniverseStatus reports the observed state of a Universe.
type UniverseStatus struct {
	// Phase is a coarse summary of the tenant state.
	// +optional
	Phase UniversePhase `json:"phase,omitempty"`
	// Namespace is the isolation namespace owned by this Universe.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// GalaxyCount is the number of Galaxies in the tenant namespace.
	// +optional
	GalaxyCount int32 `json:"galaxyCount"`
	// AstralCount is the number of Astrals in the tenant namespace.
	// +optional
	AstralCount int32 `json:"astralCount"`
	// FlareCount is the number of Flares in the tenant namespace.
	// +optional
	FlareCount int32 `json:"flareCount"`
	// LastUpdated is the time of the last successful reconciliation.
	// +optional
	LastUpdated *metav1.Time `json:"lastUpdated,omitempty"`
	// ObservedGeneration is the spec generation last processed.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions holds the machine-readable state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// NamespaceName returns the isolation namespace owned by this Universe.
func (u *Universe) NamespaceName() string {
	return NamespacePrefix + u.Name
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=uni;universes
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.status.namespace`
// +kubebuilder:printcolumn:name="Galaxies",type=integer,JSONPath=`.status.galaxyCount`
// +kubebuilder:printcolumn:name="Astrals",type=integer,JSONPath=`.status.astralCount`
// +kubebuilder:printcolumn:name="Flares",type=integer,JSONPath=`.status.flareCount`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Universe is the Schema for the universes API: the tenant boundary that every
// Galaxy, Astral and Flare is born into.
type Universe struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   UniverseSpec   `json:"spec,omitempty"`
	Status UniverseStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// UniverseList contains a list of Universe.
type UniverseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Universe `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Universe{}, &UniverseList{})
}
