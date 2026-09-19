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

// GalaxyFinalizer keeps the shared workspace and backend ConfigMap alive until
// every Astral in the Galaxy is gone.
const GalaxyFinalizer = "core.stellarcd.io/galaxy-cleanup"

// GalaxyPhase summarises a Galaxy at a glance.
type GalaxyPhase string

const (
	// GalaxyPhasePending means the parent Universe or the credentials are not ready.
	GalaxyPhasePending GalaxyPhase = "Pending"
	// GalaxyPhaseActive means the Galaxy is usable by its Astrals.
	GalaxyPhaseActive GalaxyPhase = "Active"
	// GalaxyPhaseArchived means spec.archived is set: the Galaxy is read-only.
	GalaxyPhaseArchived GalaxyPhase = "Archived"
	// GalaxyPhaseDeleting means the finalizer is draining the Galaxy.
	GalaxyPhaseDeleting GalaxyPhase = "Deleting"
)

// Galaxy-specific condition types.
const (
	// ConditionCredentialsResolved is True once the VCS Secret exists and is readable.
	ConditionCredentialsResolved = "CredentialsResolved"
	// ConditionBackendReady is True once the remote state backend config is materialised.
	ConditionBackendReady = "BackendReady"
)

// GalaxySpec defines the desired state of a Galaxy.
//
// A Galaxy groups Astrals that share a team, a VCS provider, an engine version
// and a state backend. It is the level at which credentials and approval rules
// are set once and inherited downwards.
type GalaxySpec struct {
	// UniverseRef is the name of the owning Universe. It is immutable: moving a
	// Galaxy between tenants would move its namespace too.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	UniverseRef string `json:"universeRef"`
	// DisplayName is the human-readable group name shown in the dashboard.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	DisplayName string `json:"displayName,omitempty"`
	// Description explains the scope of this group.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Description string `json:"description,omitempty"`
	// TeamEmail is the distribution list notified about Flares in this Galaxy.
	// +kubebuilder:validation:Pattern=`^$|^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$`
	// +optional
	TeamEmail string `json:"teamEmail,omitempty"`
	// Labels are propagated onto the Astrals of this Galaxy.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// DefaultVCSProvider is inherited by Astrals that do not set their own.
	// +kubebuilder:validation:Enum=GitHub;GitLab;Gitea
	// +kubebuilder:default=GitHub
	// +optional
	DefaultVCSProvider VCSProvider `json:"defaultVCSProvider,omitempty"`
	// DefaultVCSURL is the base URL of the VCS instance, used to build webhook
	// endpoints and to default Astral repositories.
	// +optional
	DefaultVCSURL string `json:"defaultVCSURL,omitempty"`
	// VCSSecretRef references the Secret holding the VCS credentials shared by
	// the Astrals of this Galaxy.
	// +optional
	VCSSecretRef *SecretRef `json:"vcsSecretRef,omitempty"`
	// Terrarium pins the engine and version used across the Galaxy.
	// +optional
	Terrarium TerrariumSpec `json:"terrarium,omitempty"`
	// BackendConfig describes the remote state backend shared by the Astrals.
	// +optional
	BackendConfig *BackendConfig `json:"backendConfig,omitempty"`
	// ApprovalPolicy decides whether Flares need a human before they run.
	// +kubebuilder:validation:Enum=Auto;Manual;RequiresReview
	// +kubebuilder:default=RequiresReview
	// +optional
	ApprovalPolicy ApprovalPolicy `json:"approvalPolicy,omitempty"`
	// Archived freezes the Galaxy: no new Flare is admitted.
	// +kubebuilder:default=false
	// +optional
	Archived bool `json:"archived,omitempty"`
	// Discovery turns a repository into Astrals automatically, instead of
	// declaring each root module by hand.
	// +optional
	Discovery *DiscoverySpec `json:"discovery,omitempty"`
}

// DiscoverySpec configures automatic Astral discovery.
//
// A repository holding hundreds of root modules is tedious and error-prone to
// transcribe into Astrals by hand. Discovery walks the repository tree on a
// schedule and materialises one Astral per module directory it finds, so the
// repository stays the source of truth for what exists.
type DiscoverySpec struct {
	// Enabled turns the routine on. It is off by default: discovery creates
	// objects, and that should be an explicit decision.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// Repository is the repository to scan. SecretRef defaults to the Galaxy's
	// vcsSecretRef, and Path scopes the walk to a subtree.
	Repository VCSRepository `json:"repository"`
	// Interval paces the walk. Discovery runs inside the Galaxy's normal
	// reconcile loop but is rate-limited to this interval, so shortening the
	// reconcile period does not multiply calls to the VCS API.
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`
	// Include keeps only modules whose directory matches one of these patterns.
	// A pattern matches when it equals the directory, is a parent prefix of it,
	// or matches it as a shell glob. Empty means "every module found".
	// +optional
	Include []string `json:"include,omitempty"`
	// Exclude drops modules matching these patterns, and wins over Include.
	// Vendored module libraries usually belong here.
	// +optional
	Exclude []string `json:"exclude,omitempty"`
	// StripPrefix is removed from a module path before its Astral name is
	// derived, which keeps names readable when every module sits under a
	// common directory.
	// +optional
	StripPrefix string `json:"stripPrefix,omitempty"`
	// MaxAstrals caps how many Astrals one pass may create. It is a guard, not
	// a target: a repository with more matching modules than this reports the
	// overflow on the Discovered condition rather than silently truncating.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1000
	// +kubebuilder:default=50
	// +optional
	MaxAstrals int32 `json:"maxAstrals,omitempty"`
	// Prune deletes previously discovered Astrals whose module has disappeared
	// from the repository. It only ever touches Astrals this Galaxy created,
	// never hand-written ones, and is off by default because deleting an Astral
	// discards its Flare history.
	// +kubebuilder:default=false
	// +optional
	Prune bool `json:"prune,omitempty"`
	// Template supplies the fields a discovered Astral cannot infer from the
	// repository layout.
	// +optional
	Template *DiscoveredAstralTemplate `json:"template,omitempty"`
}

// DiscoveredAstralTemplate is stamped onto every Astral discovery creates.
type DiscoveredAstralTemplate struct {
	// AutoApply lets a successful Plan chain into an Apply.
	// +kubebuilder:default=false
	// +optional
	AutoApply bool `json:"autoApply,omitempty"`
	// PlanOnPR raises a Plan Flare when a pull request touches the module.
	// +kubebuilder:default=true
	// +optional
	PlanOnPR bool `json:"planOnPR,omitempty"`
	// ApplyOnMerge raises an Apply Flare when the tracked branch moves.
	// +kubebuilder:default=false
	// +optional
	ApplyOnMerge bool `json:"applyOnMerge,omitempty"`
	// Labels are added to every discovered Astral.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// DriftDetection configures the periodic Refresh Flares.
	// +optional
	DriftDetection *DriftDetectionSpec `json:"driftDetection,omitempty"`
}

// DiscoveryInterval returns the configured walk interval, or the fallback when
// discovery is off or the interval is unset.
func (s *DiscoverySpec) DiscoveryInterval(fallback metav1.Duration) metav1.Duration {
	if s == nil || s.Interval == nil || s.Interval.Duration <= 0 {
		return fallback
	}
	return *s.Interval
}

// GalaxyStatus reports the observed state of a Galaxy.
type GalaxyStatus struct {
	// Phase is a coarse summary of the group state.
	// +optional
	Phase GalaxyPhase `json:"phase,omitempty"`
	// AstralCount is the number of Astrals belonging to this Galaxy.
	// +optional
	AstralCount int32 `json:"astralCount"`
	// FlareCount is the number of Flares belonging to this Galaxy.
	// +optional
	FlareCount int32 `json:"flareCount"`
	// BackendConfigMap names the ConfigMap holding the rendered backend
	// configuration consumed by the executor.
	// +optional
	BackendConfigMap string `json:"backendConfigMap,omitempty"`
	// LastSync is the time of the last successful reconciliation.
	// +optional
	LastSync *metav1.Time `json:"lastSync,omitempty"`
	// DiscoveredAstrals is how many Astrals the last discovery pass owns.
	// +optional
	DiscoveredAstrals int32 `json:"discoveredAstrals"`
	// LastDiscoveryTime is when the repository was last walked. It is what
	// rate-limits the routine across reconciles.
	// +optional
	LastDiscoveryTime *metav1.Time `json:"lastDiscoveryTime,omitempty"`
	// ObservedGeneration is the spec generation last processed.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions holds the machine-readable state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// RequiresApproval reports whether a Flare running the given action needs an
// explicit spec.approved before the controller may start it.
func (s *GalaxySpec) RequiresApproval(action FlareAction) bool {
	switch s.ApprovalPolicy {
	case ApprovalPolicyAuto:
		return false
	case ApprovalPolicyManual:
		return true
	case ApprovalPolicyRequiresReview:
		return action.Mutates()
	default:
		// An unset or unknown policy is treated as the safest one.
		return action.Mutates()
	}
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=gal;galaxies
// +kubebuilder:printcolumn:name="Universe",type=string,JSONPath=`.spec.universeRef`
// +kubebuilder:printcolumn:name="Engine",type=string,JSONPath=`.spec.terrarium.engine`
// +kubebuilder:printcolumn:name="Approval",type=string,JSONPath=`.spec.approvalPolicy`
// +kubebuilder:printcolumn:name="Astrals",type=integer,JSONPath=`.status.astralCount`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Galaxy is the Schema for the galaxies API: a logical cluster of Astrals
// orbiting one team, one VCS provider and one state backend.
type Galaxy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GalaxySpec   `json:"spec,omitempty"`
	Status GalaxyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GalaxyList contains a list of Galaxy.
type GalaxyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Galaxy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Galaxy{}, &GalaxyList{})
}
