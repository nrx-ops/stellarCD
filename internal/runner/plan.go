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

package runner

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
)

// planDocument is the part of "show -json <plan>" stellarCD reads. Only
// addresses and actions are decoded: the before/after attribute values can hold
// secrets and are never touched.
type planDocument struct {
	ResourceChanges []planChange `json:"resource_changes"`
	ResourceDrift   []planChange `json:"resource_drift"`
}

type planChange struct {
	Address string `json:"address"`
	Mode    string `json:"mode"`
	Change  struct {
		Actions []string `json:"actions"`
	} `json:"change"`
}

// PlanReport is what one plan file says, reduced to what the status needs.
type PlanReport struct {
	Summary corev1alpha1.PlanSummary
	Changes []corev1alpha1.ResourceChange
	// Destroys are the unit-qualified addresses deleted or replaced.
	Destroys []string
}

// ParsePlan reads a JSON plan. For a refresh-only plan the interesting part is
// resource_drift, the changes made outside the engine; a normal plan reports
// what applying the code would do in resource_changes. The key is simply absent
// from the document when there is nothing in it.
func ParsePlan(data []byte, unit string, refreshOnly bool) (*PlanReport, error) {
	var doc planDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("failed to decode the JSON plan: %w", err)
	}
	entries := doc.ResourceChanges
	if refreshOnly {
		entries = doc.ResourceDrift
	}

	report := &PlanReport{}
	for _, entry := range entries {
		if entry.Mode == "data" {
			continue
		}
		action, ok := classify(entry.Change.Actions)
		if !ok {
			continue
		}
		switch action {
		case corev1alpha1.ChangeActionCreate:
			report.Summary.ToCreate++
		case corev1alpha1.ChangeActionUpdate:
			report.Summary.ToUpdate++
		case corev1alpha1.ChangeActionDelete:
			report.Summary.ToDestroy++
			report.Destroys = append(report.Destroys, QualifiedAddress(unit, entry.Address))
		case corev1alpha1.ChangeActionReplace:
			report.Summary.ToReplace++
			report.Destroys = append(report.Destroys, QualifiedAddress(unit, entry.Address))
		}
		report.Changes = append(report.Changes, corev1alpha1.ResourceChange{
			Address: entry.Address,
			Action:  action,
			Unit:    unit,
		})
	}
	return report, nil
}

// classify maps the engine's action list onto stellarCD's. No-op, read and
// forget change nothing in real infrastructure and are dropped. A replacement
// is reported as ["delete","create"] or ["create","delete"] depending on
// create_before_destroy; either way the old resource is destroyed, so the
// destroy protection treats it as a destruction.
func classify(actions []string) (corev1alpha1.ChangeAction, bool) {
	switch {
	case slices.Contains(actions, "delete") && slices.Contains(actions, "create"):
		return corev1alpha1.ChangeActionReplace, true
	case slices.Contains(actions, "delete"):
		return corev1alpha1.ChangeActionDelete, true
	case slices.Contains(actions, "create"):
		return corev1alpha1.ChangeActionCreate, true
	case slices.Contains(actions, "update"):
		return corev1alpha1.ChangeActionUpdate, true
	default:
		return "", false
	}
}

// QualifiedAddress makes an address unique across the units of a RunAll
// Astral, where two units commonly hold resources with the same address.
func QualifiedAddress(unit, address string) string {
	if unit == "" {
		return address
	}
	return unit + "::" + address
}

// UnconfirmedDestroys returns the destroyed resources the user has not
// confirmed. The comparison is on the exact set: a confirmation given for one
// plan does not stretch to cover a different resource in the next one.
func UnconfirmedDestroys(destroys, allowed []string) []string {
	confirmed := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		confirmed[a] = struct{}{}
	}
	var out []string
	for _, d := range destroys {
		if _, ok := confirmed[d]; !ok {
			out = append(out, d)
		}
	}
	return out
}

// DescribeSummary renders a summary for the one-line status message.
func DescribeSummary(s *corev1alpha1.PlanSummary) string {
	if !s.HasChanges() {
		return "no changes"
	}
	parts := make([]string, 0, 4)
	for _, p := range []struct {
		n    int32
		verb string
	}{
		{s.ToCreate, "to create"},
		{s.ToUpdate, "to update"},
		{s.ToDestroy, "to destroy"},
		{s.ToReplace, "to replace"},
	} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.verb))
		}
	}
	return strings.Join(parts, ", ")
}
