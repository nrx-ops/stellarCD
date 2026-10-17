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
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
)

// flareAt builds a terminal Flare finished at minute m; tests list them newest
// first, the order listFlares returns.
func flareAt(name string, m int, action corev1alpha1.FlareAction, phase corev1alpha1.FlarePhase,
	summary *corev1alpha1.PlanSummary) corev1alpha1.Flare {
	t := metav1.NewTime(time.Date(2026, 10, 17, 10, m, 0, 0, time.UTC))
	f := corev1alpha1.Flare{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: t},
		Spec:       corev1alpha1.FlareSpec{Action: action, Scope: corev1alpha1.FlareScopeAll},
		Status:     corev1alpha1.FlareStatus{Phase: phase, PlanSummary: summary, Revision: name + "-sha"},
	}
	if phase.IsTerminal() {
		f.Status.CompletionTime = &t
	}
	return f
}

func TestFoldSyncSingleState(t *testing.T) {
	changes := &corev1alpha1.PlanSummary{ToCreate: 1, ToDestroy: 2}
	tests := []struct {
		name     string
		flares   []corev1alpha1.Flare
		want     corev1alpha1.SyncStatus
		destroy  int32
		revision string
	}{
		{
			name: "nothing planned yet",
			want: corev1alpha1.SyncStatusUnknown,
		},
		{
			name:    "plan found changes",
			flares:  []corev1alpha1.Flare{flareAt("p1", 1, corev1alpha1.FlareActionPlan, corev1alpha1.FlarePhaseSucceeded, changes)},
			want:    corev1alpha1.SyncStatusOutOfSync,
			destroy: 2,
		},
		{
			name: "applied since",
			flares: []corev1alpha1.Flare{
				flareAt("a1", 2, corev1alpha1.FlareActionApply, corev1alpha1.FlarePhaseSucceeded, changes),
				flareAt("p1", 1, corev1alpha1.FlareActionPlan, corev1alpha1.FlarePhaseSucceeded, changes),
			},
			want:     corev1alpha1.SyncStatusSynced,
			revision: "a1-sha",
		},
		{
			// A failure says nothing about alignment: the earlier plan stands.
			name: "newest run failed",
			flares: []corev1alpha1.Flare{
				flareAt("p2", 2, corev1alpha1.FlareActionPlan, corev1alpha1.FlarePhaseFailed, nil),
				flareAt("p1", 1, corev1alpha1.FlareActionPlan, corev1alpha1.FlarePhaseSucceeded, changes),
			},
			want:    corev1alpha1.SyncStatusOutOfSync,
			destroy: 2,
		},
		{
			// An apply held for confirmation carries a fresh plan.
			name: "apply awaiting destroy confirmation",
			flares: []corev1alpha1.Flare{
				flareAt("a1", 2, corev1alpha1.FlareActionApply, corev1alpha1.FlarePhaseAwaitingConfirmation, changes),
				flareAt("p0", 1, corev1alpha1.FlareActionPlan, corev1alpha1.FlarePhaseSucceeded, &corev1alpha1.PlanSummary{}),
			},
			want:    corev1alpha1.SyncStatusOutOfSync,
			destroy: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			astral := &corev1alpha1.Astral{}
			(&AstralReconciler{}).foldSync(astral, tc.flares)
			if astral.Status.SyncStatus != tc.want {
				t.Fatalf("got %q, want %q", astral.Status.SyncStatus, tc.want)
			}
			if tc.destroy > 0 && astral.Status.Drift.ToDestroy != tc.destroy {
				t.Fatalf("got drift %+v, want %d to destroy", astral.Status.Drift, tc.destroy)
			}
			if astral.Status.LastSyncedRevision != tc.revision {
				t.Fatalf("got last synced revision %q, want %q", astral.Status.LastSyncedRevision, tc.revision)
			}
		})
	}
}

func TestFoldSyncRunAll(t *testing.T) {
	runAll := func() *corev1alpha1.Astral {
		return &corev1alpha1.Astral{Spec: corev1alpha1.AstralSpec{Terragrunt: &corev1alpha1.TerragruntSpec{
			Enabled: true, Mode: corev1alpha1.TerragruntModeRunAll,
		}}}
	}
	stack := flareAt("all", 1, corev1alpha1.FlareActionPlan, corev1alpha1.FlarePhaseSucceeded,
		&corev1alpha1.PlanSummary{ToUpdate: 1})
	stack.Status.UnitResults = []corev1alpha1.FlareUnitResult{
		{Path: "apps/web", Result: corev1alpha1.UnitResultSucceeded, PlanSummary: &corev1alpha1.PlanSummary{}},
		{Path: "apps/api", Result: corev1alpha1.UnitResultSucceeded, PlanSummary: &corev1alpha1.PlanSummary{ToUpdate: 1}},
		{Path: "data/db", Result: corev1alpha1.UnitResultFailed, Message: "boom"},
	}

	t.Run("worst unit wins", func(t *testing.T) {
		astral := runAll()
		(&AstralReconciler{}).foldSync(astral, []corev1alpha1.Flare{stack})
		if astral.Status.SyncStatus != corev1alpha1.SyncStatusOutOfSync {
			t.Fatalf("got %q", astral.Status.SyncStatus)
		}
		if len(astral.Status.Units) != 3 || astral.Status.Drift.ToUpdate != 1 {
			t.Fatalf("got units %+v drift %+v", astral.Status.Units, astral.Status.Drift)
		}
	})

	t.Run("all units synced is synced", func(t *testing.T) {
		clean := flareAt("clean", 1, corev1alpha1.FlareActionPlan, corev1alpha1.FlarePhaseSucceeded, &corev1alpha1.PlanSummary{})
		clean.Status.UnitResults = []corev1alpha1.FlareUnitResult{
			{Path: "a", Result: corev1alpha1.UnitResultSucceeded, PlanSummary: &corev1alpha1.PlanSummary{}},
			{Path: "b", Result: corev1alpha1.UnitResultSucceeded, PlanSummary: &corev1alpha1.PlanSummary{}},
		}
		astral := runAll()
		(&AstralReconciler{}).foldSync(astral, []corev1alpha1.Flare{clean})
		if astral.Status.SyncStatus != corev1alpha1.SyncStatusSynced {
			t.Fatalf("got %q, a stack with every unit Synced must read Synced", astral.Status.SyncStatus)
		}
	})

	t.Run("a newer unit-scoped plan fixes only its unit", func(t *testing.T) {
		fix := flareAt("fix", 2, corev1alpha1.FlareActionPlan, corev1alpha1.FlarePhaseSucceeded, &corev1alpha1.PlanSummary{})
		fix.Spec.Scope = corev1alpha1.FlareScopeUnit
		fix.Spec.UnitPath = "data/db"

		astral := runAll()
		(&AstralReconciler{}).foldSync(astral, []corev1alpha1.Flare{fix, stack})
		units := map[string]corev1alpha1.UnitStatus{}
		for _, u := range astral.Status.Units {
			units[u.Path] = u
		}
		if units["data/db"].Result != corev1alpha1.UnitResultSucceeded || units["data/db"].LastFlare != "fix" {
			t.Fatalf("the unit plan did not replace the failure: %+v", units["data/db"])
		}
		if units["apps/api"].LastFlare != "all" || units["apps/api"].SyncStatus != corev1alpha1.SyncStatusOutOfSync {
			t.Fatalf("a unit-scoped Flare must leave the other units alone: %+v", units["apps/api"])
		}
		if len(failedUnitPaths(astral)) != 0 {
			t.Fatal("the run-all block must lift once the failed unit plans cleanly")
		}
	})

	t.Run("an excluded unit keeps its older result", func(t *testing.T) {
		newer := flareAt("newer", 2, corev1alpha1.FlareActionPlan, corev1alpha1.FlarePhaseSucceeded, &corev1alpha1.PlanSummary{})
		newer.Status.UnitResults = []corev1alpha1.FlareUnitResult{
			{Path: "apps/web", Result: corev1alpha1.UnitResultSucceeded, PlanSummary: &corev1alpha1.PlanSummary{}},
			{Path: "data/db", Result: corev1alpha1.UnitResultExcluded},
		}
		astral := runAll()
		(&AstralReconciler{}).foldSync(astral, []corev1alpha1.Flare{newer, stack})
		for _, u := range astral.Status.Units {
			if u.Path == "data/db" && (u.LastFlare != "all" || u.Result != corev1alpha1.UnitResultFailed) {
				t.Fatalf("excluding a unit must not erase what is known about it: %+v", u)
			}
		}
	})
}

func TestSyncStatusWorse(t *testing.T) {
	s, o, u := corev1alpha1.SyncStatusSynced, corev1alpha1.SyncStatusOutOfSync, corev1alpha1.SyncStatusUnknown
	tests := []struct{ a, b, want corev1alpha1.SyncStatus }{
		{s, s, s}, {s, u, u}, {u, s, u}, {s, o, o}, {u, o, o}, {o, s, o},
	}
	for _, tc := range tests {
		if got := tc.a.Worse(tc.b); got != tc.want {
			t.Errorf("%s.Worse(%s) = %s, want %s", tc.a, tc.b, got, tc.want)
		}
	}
}
