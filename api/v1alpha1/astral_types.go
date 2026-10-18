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
	"k8s.io/apimachinery/pkg/runtime"
)

// AstralFinalizer holds an Astral until its Flare history is reclaimed and, when
// requested, its infrastructure has been destroyed.
const AstralFinalizer = "core.stellarcd.io/astral-cleanup"

// MaxFlareHistory bounds status.flareHistory. Status is not an audit log: the
// full record lives in the Flare objects themselves.
const MaxFlareHistory = 10

// Astral-specific condition types.
const (
	// ConditionConfigValid is True once the repository and engine settings are coherent.
	ConditionConfigValid = "ConfigValid"
	// ConditionDependenciesReady is True once every Astral in spec.dependsOn is Ready.
	ConditionDependenciesReady = "DependenciesReady"
	// ConditionDriftFree is True while no drift has been detected.
	ConditionDriftFree = "DriftFree"
)

// AstralHooks holds shell commands run around the engine invocation. Commands
// execute in the module directory with the same environment as the engine, so
// they must never echo the injected credentials.
type AstralHooks struct {
	// PrePlan runs before "plan".
	// +optional
	PrePlan []string `json:"prePlan,omitempty"`
	// PostPlan runs after a successful "plan".
	// +optional
	PostPlan []string `json:"postPlan,omitempty"`
	// PreApply runs before "apply".
	// +optional
	PreApply []string `json:"preApply,omitempty"`
	// PostApply runs after a successful "apply".
	// +optional
	PostApply []string `json:"postApply,omitempty"`
}

// DriftDetectionSpec configures the periodic Plan Flares that compare real
// infrastructure against the tracked revision.
type DriftDetectionSpec struct {
	// Enabled turns periodic drift detection on.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// Interval is the delay between two drift checks.
	// +kubebuilder:default="1h"
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`
}

// AstralSpec defines the desired state of an Astral.
//
// An Astral is one Terraform/OpenTofu/Terragrunt root module: one directory,
// one workspace, one state. Every run against it is a Flare.
type AstralSpec struct {
	// UniverseRef is the name of the owning Universe. Immutable.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	UniverseRef string `json:"universeRef"`
	// GalaxyRef is the name of the owning Galaxy, resolved in the same
	// namespace as this Astral. Immutable.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	GalaxyRef string `json:"galaxyRef"`
	// DisplayName is the human-readable project name shown in the dashboard.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	DisplayName string `json:"displayName,omitempty"`
	// Description explains what this project provisions.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Description string `json:"description,omitempty"`
	// VCSRepository locates the root module.
	VCSRepository VCSRepository `json:"vcsRepository"`
	// Engine is the binary that runs init, plan and apply and owns the state.
	// +kubebuilder:default=Terraform
	// +optional
	Engine Engine `json:"engine,omitempty"`
	// EngineVersion is the exact engine version, e.g. "1.16.6". Empty means the
	// version the runner image ships; a mismatch fails the Flare before init.
	// +kubebuilder:validation:Pattern=`^$|^v?[0-9]+\.[0-9]+(\.[0-9]+)?$`
	// +optional
	EngineVersion string `json:"engineVersion,omitempty"`
	// Terragrunt puts Terragrunt in front of the engine.
	// +optional
	Terragrunt *TerragruntSpec `json:"terragrunt,omitempty"`
	// Variables are non-sensitive inputs passed as TF_VAR_<name>. Anything
	// secret belongs in spec.secrets instead.
	// +optional
	Variables map[string]string `json:"variables,omitempty"`
	// Secrets project Kubernetes Secrets into the executor environment.
	// +optional
	Secrets []EnvSecretRef `json:"secrets,omitempty"`
	// BackendSecretRef names the Secret holding the state backend
	// credentials, e.g. GOOGLE_CREDENTIALS for GCS. Its keys become env vars
	// verbatim. It overrides the Galaxy's spec.backendConfig.secretRef.
	// +optional
	BackendSecretRef *SecretRef `json:"backendSecretRef,omitempty"`
	// WorkspaceName is the engine workspace selected before every run.
	// +kubebuilder:default="default"
	// +kubebuilder:validation:MinLength=1
	// +optional
	WorkspaceName string `json:"workspaceName,omitempty"`
	// EnableLocking passes -lock to the engine, so it takes the backend state
	// lock. It does not affect stellarCD's own rule of one active Flare per
	// Astral, which always applies. Turning it off is only safe for read-only
	// modules.
	// +kubebuilder:default=true
	// +optional
	EnableLocking bool `json:"enableLocking,omitempty"`
	// AutoApply lets a successful Plan chain straight into an Apply.
	// +kubebuilder:default=false
	// +optional
	AutoApply bool `json:"autoApply,omitempty"`
	// PlanOnPR raises a Plan Flare when a pull request touches spec.vcsRepository.path.
	// +kubebuilder:default=true
	// +optional
	PlanOnPR bool `json:"planOnPR,omitempty"`
	// ApplyOnMerge raises an Apply Flare when the tracked branch moves.
	// +kubebuilder:default=false
	// +optional
	ApplyOnMerge bool `json:"applyOnMerge,omitempty"`
	// CustomHooks are shell commands run around the engine invocation.
	// +optional
	CustomHooks *AstralHooks `json:"customHooks,omitempty"`
	// DependsOn lists Astral names in the same namespace that must be Synced
	// before this one may run. Cycles are rejected by the validating webhook.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`
	// DriftDetection configures the periodic Plan Flares that keep syncStatus
	// and the drift counts current.
	// +optional
	DriftDetection *DriftDetectionSpec `json:"driftDetection,omitempty"`
}

// FlareRef is one entry of the rolling Flare history kept on an Astral.
type FlareRef struct {
	// Name is the Flare object name.
	Name string `json:"name"`
	// Action is the action that Flare performed.
	// +optional
	Action FlareAction `json:"action,omitempty"`
	// Phase is the terminal phase that Flare reached.
	// +optional
	Phase FlarePhase `json:"phase,omitempty"`
	// CompletionTime is when that Flare finished.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
}

// InfrastructureStatus is the last known shape of the managed infrastructure.
type InfrastructureStatus struct {
	// ResourceCount is the number of resources in the state file.
	// +optional
	ResourceCount int32 `json:"resourceCount"`
	// EstimatedMonthlyCost is a decimal amount, e.g. "412.50". It is a string
	// rather than a float because the Kubernetes API conventions rule out
	// floating point fields: they round-trip badly across clients.
	// +kubebuilder:validation:Pattern=`^$|^[0-9]+(\.[0-9]{1,2})?$`
	// +optional
	EstimatedMonthlyCost string `json:"estimatedMonthlyCost,omitempty"`
	// Currency is the ISO 4217 code for EstimatedMonthlyCost.
	// +kubebuilder:validation:Pattern=`^$|^[A-Z]{3}$`
	// +optional
	Currency string `json:"currency,omitempty"`
}

// DriftStatus records the outcome of the last plan: whether real
// infrastructure diverges from the tracked revision, and by how much. In
// RunAll mode the counts are summed over the units.
type DriftStatus struct {
	// Detected is true while real infrastructure diverges from the tracked revision.
	// +optional
	Detected bool `json:"detected"`
	// PlanSummary is what the last plan would change.
	// +optional
	PlanSummary `json:",inline"`
	// LastDetected is when drift was last observed.
	// +optional
	LastDetected *metav1.Time `json:"lastDetected,omitempty"`
	// LastChecked is when the last drift check ran, drifted or not.
	// +optional
	LastChecked *metav1.Time `json:"lastChecked,omitempty"`
}

// UnitResult is the outcome of one Terragrunt unit in a RunAll Flare.
// +kubebuilder:validation:Enum=Succeeded;Failed;Skipped;Excluded
type UnitResult string

const (
	// UnitResultSucceeded means the unit planned or applied cleanly.
	UnitResultSucceeded UnitResult = "Succeeded"
	// UnitResultFailed means the unit's own run failed.
	UnitResultFailed UnitResult = "Failed"
	// UnitResultSkipped means the unit never ran because a unit it depends on failed.
	UnitResultSkipped UnitResult = "Skipped"
	// UnitResultExcluded means the unit is listed in terragrunt.excludedUnits.
	UnitResultExcluded UnitResult = "Excluded"
)

// UnitStatus is the per-unit view of a RunAll Astral.
type UnitStatus struct {
	// Path is the unit directory, relative to the Astral path.
	Path string `json:"path"`
	// SyncStatus is the unit's own alignment with the tracked revision.
	// +optional
	SyncStatus SyncStatus `json:"syncStatus,omitempty"`
	// Result is the outcome of the last Flare that ran this unit.
	// +optional
	Result UnitResult `json:"result,omitempty"`
	// Drift is what the last plan would change in this unit.
	// +optional
	Drift *PlanSummary `json:"drift,omitempty"`
	// LastFlare is the last Flare that ran this unit.
	// +optional
	LastFlare string `json:"lastFlare,omitempty"`
	// Message explains a failed or skipped unit.
	// +optional
	Message string `json:"message,omitempty"`
}

// AstralStatus reports the observed state of an Astral.
type AstralStatus struct {
	// SyncStatus says whether real infrastructure matches the tracked revision.
	// It replaces the former phase, which mixed what is running with what the
	// result is: progress is now read from activeFlare, failures from the
	// Reconciled condition and lastFlareStatus. In RunAll mode it is the worst
	// status among the units.
	// +optional
	SyncStatus SyncStatus `json:"syncStatus,omitempty"`
	// ActiveFlare is the Flare currently running against this Astral, if any.
	// +optional
	ActiveFlare string `json:"activeFlare,omitempty"`
	// LastFlareRef is the most recent Flare that reached a terminal phase.
	// +optional
	LastFlareRef string `json:"lastFlareRef,omitempty"`
	// LastSyncedRevision is the commit last applied successfully to the whole Astral.
	// +optional
	LastSyncedRevision string `json:"lastSyncedRevision,omitempty"`
	// LastSyncTime is when that apply finished.
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`
	// Units is the per-unit view, in RunAll mode only.
	// +listType=map
	// +listMapKey=path
	// +optional
	Units []UnitStatus `json:"units,omitempty"`
	// LastSuccessfulFlare is the name of the last Flare that succeeded.
	// +optional
	LastSuccessfulFlare string `json:"lastSuccessfulFlare,omitempty"`
	// LastFlareStatus is the terminal phase of the most recent Flare.
	// +kubebuilder:validation:Enum=Succeeded;Failed;Cancelled
	// +optional
	LastFlareStatus FlarePhase `json:"lastFlareStatus,omitempty"`
	// FlareHistory keeps the most recent finished Flares, newest first.
	// +kubebuilder:validation:MaxItems=10
	// +optional
	FlareHistory []FlareRef `json:"flareHistory,omitempty"`
	// Infrastructure is the last known shape of the managed resources.
	// +optional
	Infrastructure *InfrastructureStatus `json:"infrastructure,omitempty"`
	// Drift records the outcome of the last plan.
	// +optional
	Drift *DriftStatus `json:"drift,omitempty"`
	// ObservedGeneration is the spec generation last processed.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions holds the machine-readable state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// BackendSecret returns the Secret holding the state backend credentials: the
// Astral's own, else the Galaxy's. Nil means the run gets none.
func (s *AstralSpec) BackendSecret(galaxy *GalaxySpec) *SecretRef {
	if s.BackendSecretRef != nil {
		return s.BackendSecretRef
	}
	if galaxy != nil && galaxy.BackendConfig != nil {
		return galaxy.BackendConfig.SecretRef
	}
	return nil
}

// DriftInterval returns the configured drift-check interval, or zero when drift
// detection is disabled.
func (s *AstralSpec) DriftInterval(fallback metav1.Duration) metav1.Duration {
	if s.DriftDetection == nil || !s.DriftDetection.Enabled {
		return metav1.Duration{}
	}
	if s.DriftDetection.Interval == nil || s.DriftDetection.Interval.Duration <= 0 {
		return fallback
	}
	return *s.DriftDetection.Interval
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ast;astrals
// +kubebuilder:printcolumn:name="Galaxy",type=string,JSONPath=`.spec.galaxyRef`
// +kubebuilder:printcolumn:name="Repository",type=string,JSONPath=`.spec.vcsRepository.url`
// +kubebuilder:printcolumn:name="Path",type=string,JSONPath=`.spec.vcsRepository.path`
// +kubebuilder:printcolumn:name="Sync",type=string,JSONPath=`.status.syncStatus`
// +kubebuilder:printcolumn:name="Destroy",type=integer,JSONPath=`.status.drift.toDestroy`
// +kubebuilder:printcolumn:name="Active",type=string,JSONPath=`.status.activeFlare`
// +kubebuilder:printcolumn:name="Last Flare",type=string,JSONPath=`.status.lastSuccessfulFlare`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Astral is the Schema for the astrals API: one root module, one state, one
// plane of infrastructure that Flares illuminate.
type Astral struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AstralSpec   `json:"spec,omitempty"`
	Status AstralStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AstralList contains a list of Astral.
type AstralList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Astral `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Astral{}, &AstralList{})
		return nil
	})
}
