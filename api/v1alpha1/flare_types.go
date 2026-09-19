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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FlareFinalizer keeps a Flare around until its executor process has been
// signalled and its artifacts reclaimed, so a delete never orphans a running
// terraform apply.
const FlareFinalizer = "core.stellarcd.io/flare-cleanup"

// DefaultFlareTimeoutSeconds bounds a single run. Terraform applies that need
// more than an hour should be split, not given a longer leash.
const DefaultFlareTimeoutSeconds int32 = 3600

// DefaultFlareParallelism matches the engine's own default.
const DefaultFlareParallelism int32 = 10

// FlareAction is the engine operation a Flare performs.
type FlareAction string

const (
	FlareActionPlan    FlareAction = "Plan"
	FlareActionApply   FlareAction = "Apply"
	FlareActionDestroy FlareAction = "Destroy"
	FlareActionRefresh FlareAction = "Refresh"
)

// Mutates reports whether the action can change real infrastructure. It drives
// the RequiresReview approval policy and the Astral phase during the run.
func (a FlareAction) Mutates() bool {
	return a == FlareActionApply || a == FlareActionDestroy
}

// FlarePhase summarises a run at a glance.
type FlarePhase string

const (
	// FlarePhasePending means the run is admitted but not started.
	FlarePhasePending FlarePhase = "Pending"
	// FlarePhaseAwaitingApproval means the Galaxy approval policy is holding the run.
	FlarePhaseAwaitingApproval FlarePhase = "AwaitingApproval"
	// FlarePhaseRunning means the engine process is live.
	FlarePhaseRunning FlarePhase = "Running"
	// FlarePhaseSucceeded is terminal.
	FlarePhaseSucceeded FlarePhase = "Succeeded"
	// FlarePhaseFailed is terminal.
	FlarePhaseFailed FlarePhase = "Failed"
	// FlarePhaseCancelled is terminal: spec.cancel was set, or the timeout fired.
	FlarePhaseCancelled FlarePhase = "Cancelled"
)

// IsTerminal reports whether the phase can no longer change.
func (p FlarePhase) IsTerminal() bool {
	return p == FlarePhaseSucceeded || p == FlarePhaseFailed || p == FlarePhaseCancelled
}

// Flare-specific condition types.
const (
	// ConditionApproved is True once the run is cleared to start.
	ConditionApproved = "Approved"
	// ConditionStarted is True once the engine process has been launched.
	ConditionStarted = "Started"
	// ConditionCompleted is True once the run reached a terminal phase.
	ConditionCompleted = "Completed"
)

// FlareSpec defines the desired state of a Flare.
//
// A Flare is a single, one-shot run. Everything except approved and cancel is
// immutable after creation: re-running means creating a new Flare, which keeps
// the history honest.
type FlareSpec struct {
	// UniverseRef is the name of the owning Universe. Immutable.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	UniverseRef string `json:"universeRef"`
	// GalaxyRef is the name of the owning Galaxy. Immutable.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	GalaxyRef string `json:"galaxyRef"`
	// AstralRef is the name of the target Astral, resolved in the same
	// namespace as this Flare. Immutable.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	AstralRef string `json:"astralRef"`
	// Action is the engine operation to perform.
	// +kubebuilder:validation:Enum=Plan;Apply;Destroy;Refresh
	Action FlareAction `json:"action"`
	// Variables override the Astral variables for this run only.
	// +optional
	Variables map[string]string `json:"variables,omitempty"`
	// AutoApprove skips the approval gate. The validating webhook rejects it
	// when the Galaxy policy is Manual, so it cannot be used to bypass review.
	// +kubebuilder:default=false
	// +optional
	AutoApprove bool `json:"autoApprove,omitempty"`
	// Approved is the reviewer's decision. It is the one field a reviewer may
	// flip after creation; false explicitly rejects and cancels the run.
	// +optional
	Approved *bool `json:"approved,omitempty"`
	// Cancel asks the controller to abort a running Flare. It is one-way.
	// +kubebuilder:default=false
	// +optional
	Cancel bool `json:"cancel,omitempty"`
	// RequestedBy is the email of the human or system that raised this run.
	// +kubebuilder:validation:Pattern=`^$|^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$`
	// +optional
	RequestedBy string `json:"requestedBy,omitempty"`
	// Parallelism is the engine -parallelism flag.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=256
	// +kubebuilder:default=10
	// +optional
	Parallelism int32 `json:"parallelism,omitempty"`
	// RefreshBeforePlan runs a refresh before planning, at the cost of a full
	// provider round trip.
	// +kubebuilder:default=true
	// +optional
	RefreshBeforePlan bool `json:"refreshBeforePlan,omitempty"`
	// Tags are free-form labels carried through to notifications and metrics.
	// +optional
	Tags map[string]string `json:"tags,omitempty"`
	// TimeoutSeconds bounds the run. The engine process is killed when it fires
	// and the Flare moves to Cancelled.
	// +kubebuilder:validation:Minimum=60
	// +kubebuilder:validation:Maximum=86400
	// +kubebuilder:default=3600
	// +optional
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`
}

// FlareResourceCounts is the resource delta reported by the engine.
type FlareResourceCounts struct {
	// Created is the number of resources added.
	// +optional
	Created int32 `json:"created"`
	// Updated is the number of resources changed in place.
	// +optional
	Updated int32 `json:"updated"`
	// Deleted is the number of resources destroyed.
	// +optional
	Deleted int32 `json:"deleted"`
	// Unchanged is the number of resources left alone.
	// +optional
	Unchanged int32 `json:"unchanged"`
}

// FlareArtifacts points at the objects holding the run's binary outputs. The
// payloads live in Secrets, never inline in the status: a plan file leaks the
// same values a state file does.
type FlareArtifacts struct {
	// PlanFile names the Secret holding the binary plan produced by this run.
	// +optional
	PlanFile string `json:"planFile,omitempty"`
	// TFState names the Secret holding the state snapshot taken after the run.
	// +optional
	TFState string `json:"tfstate,omitempty"`
}

// FlareStatus reports the observed state of a Flare.
type FlareStatus struct {
	// Phase is a coarse summary of the run.
	// +optional
	Phase FlarePhase `json:"phase,omitempty"`
	// Action echoes the executed action so consumers need not read the spec.
	// +kubebuilder:validation:Enum=Plan;Apply;Destroy;Refresh
	// +optional
	Action FlareAction `json:"action,omitempty"`
	// StartTime is when the engine process was launched.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`
	// CompletionTime is when the run reached a terminal phase.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
	// Duration is CompletionTime minus StartTime, rendered for humans.
	// +optional
	Duration string `json:"duration,omitempty"`
	// PlanOutput is the truncated, human-readable plan summary.
	// +optional
	PlanOutput string `json:"planOutput,omitempty"`
	// ApplyOutput is the truncated, human-readable apply summary.
	// +optional
	ApplyOutput string `json:"applyOutput,omitempty"`
	// Errors holds the diagnostics that caused a failure.
	// +optional
	Errors []string `json:"errors,omitempty"`
	// Warnings holds non-fatal diagnostics.
	// +optional
	Warnings []string `json:"warnings,omitempty"`
	// Resources is the delta reported by the engine.
	// +optional
	Resources *FlareResourceCounts `json:"resources,omitempty"`
	// Artifacts points at the Secrets holding the plan file and state snapshot.
	// +optional
	Artifacts *FlareArtifacts `json:"artifacts,omitempty"`
	// ObservedGeneration is the spec generation last processed.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions holds the machine-readable state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Timeout returns the configured run timeout as a duration, falling back to the
// package default for objects that bypassed CRD defaulting.
func (s *FlareSpec) Timeout() metav1.Duration {
	seconds := s.TimeoutSeconds
	if seconds <= 0 {
		seconds = DefaultFlareTimeoutSeconds
	}
	return metav1.Duration{Duration: time.Duration(seconds) * time.Second}
}

// EffectiveParallelism returns the -parallelism value to pass to the engine.
func (s *FlareSpec) EffectiveParallelism() int32 {
	if s.Parallelism <= 0 {
		return DefaultFlareParallelism
	}
	return s.Parallelism
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=fl;flares
// +kubebuilder:printcolumn:name="Astral",type=string,JSONPath=`.spec.astralRef`
// +kubebuilder:printcolumn:name="Action",type=string,JSONPath=`.spec.action`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Duration",type=string,JSONPath=`.status.duration`
// +kubebuilder:printcolumn:name="Requested By",type=string,priority=1,JSONPath=`.spec.requestedBy`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Flare is the Schema for the flares API: a single burst of engine execution
// against one Astral.
type Flare struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FlareSpec   `json:"spec,omitempty"`
	Status FlareStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FlareList contains a list of Flare.
type FlareList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Flare `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Flare{}, &FlareList{})
}
