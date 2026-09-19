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

package controller

import (
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// maxConditionMessage keeps a condition message under the API server limit even
// when it carries a raw engine diagnostic.
const maxConditionMessage = 3072

// Condition reasons shared across the Universe -> Galaxy -> Astral -> Flare
// controllers. Reasons are part of the API contract, so they are constants
// rather than inline strings.
const (
	reasonReconciled       = "Reconciled"
	reasonReconcileFailed  = "ReconcileFailed"
	reasonParentMissing    = "ParentMissing"
	reasonParentNotReady   = "ParentNotReady"
	reasonParentResolved   = "ParentResolved"
	reasonSuspended        = "Suspended"
	reasonArchived         = "Archived"
	reasonDeleting         = "Deleting"
	reasonSecretMissing    = "SecretMissing"
	reasonSecretResolved   = "SecretResolved"
	reasonValidationFailed = "ValidationFailed"
)

// applyCondition upserts a condition, stamping the observed generation and
// letting apimeta preserve LastTransitionTime when nothing actually changed.
// It reports whether the condition list was modified.
func applyCondition(
	conditions *[]metav1.Condition,
	generation int64,
	condType string,
	status metav1.ConditionStatus,
	reason, message string,
) bool {
	return apimeta.SetStatusCondition(conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            trimMessage(message),
		ObservedGeneration: generation,
	})
}

// trimMessage bounds a condition message. Truncation keeps the head: the first
// line of an engine diagnostic is the one that names the failing resource.
func trimMessage(msg string) string {
	if len(msg) <= maxConditionMessage {
		return msg
	}
	return msg[:maxConditionMessage] + "…"
}

// conditionTrue is a small helper for the frequent "did the parent report
// Ready?" question.
func conditionTrue(conditions []metav1.Condition, condType string) bool {
	return apimeta.IsStatusConditionTrue(conditions, condType)
}
