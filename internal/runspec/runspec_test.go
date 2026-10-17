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

package runspec

import (
	"errors"
	"strings"
	"testing"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
)

func sampleResult(pending int) *Result {
	r := &Result{
		Succeeded:   true,
		Step:        corev1alpha1.FlareStepPlan,
		Revision:    "5c3de85c6df8226ad38ad29d77251ac306d3b9d6",
		PlanSummary: &corev1alpha1.PlanSummary{ToCreate: 1, ToDestroy: 2},
	}
	for range pending {
		r.PendingDestroy = append(r.PendingDestroy,
			"apps/x/ns::module.very.long.address.terraform_data.resource_"+strings.Repeat("x", 40))
	}
	return r
}

func TestResultRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		pending int
	}{
		{name: "single chunk", pending: 0},
		{name: "many chunks", pending: 900},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lines, err := ResultLines("n0nce", sampleResult(tc.pending))
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range lines {
				if len(l) > chunkSize+64 {
					t.Fatalf("line of %d bytes exceeds the chunk budget", len(l))
				}
			}
			log := "engine noise\n" + StepLine("n0nce", corev1alpha1.FlareStepPlan) + "\n" +
				strings.Join(lines, "\n") + "\n"

			step, got, err := ParseLog(log, "n0nce")
			if err != nil {
				t.Fatal(err)
			}
			if step != corev1alpha1.FlareStepPlan {
				t.Fatalf("got step %q", step)
			}
			if len(got.PendingDestroy) != tc.pending || got.PlanSummary.ToDestroy != 2 {
				t.Fatalf("result did not round-trip: %+v", got)
			}
		})
	}
}

// TestParseLogIgnoresForgedLines checks that a result block tagged with another
// nonce, such as one replayed from an older run's log, is not taken for this
// run's result.
func TestParseLogIgnoresForgedLines(t *testing.T) {
	forged, _ := ResultLines("guessed", &Result{Succeeded: true, Message: "forged"})
	real, _ := ResultLines("secret-nonce", &Result{Succeeded: false, Message: "real"})

	log := strings.Join(append(real, forged...), "\n")
	_, got, err := ParseLog(log, "secret-nonce")
	if err != nil {
		t.Fatal(err)
	}
	if got.Message != "real" {
		t.Fatalf("got %q, the forged result was accepted", got.Message)
	}
}

func TestParseLogWithoutResult(t *testing.T) {
	log := StepLine("n", corev1alpha1.FlareStepInit) + "\nterraform crashed\n"
	step, _, err := ParseLog(log, "n")
	if !errors.Is(err, ErrNoResult) {
		t.Fatalf("got %v, want ErrNoResult", err)
	}
	if step != corev1alpha1.FlareStepInit {
		t.Fatalf("got step %q, the last step must still be reported", step)
	}
}

// TestParseLogDropsTruncatedBlock covers a log cut mid-result, e.g. when only
// the tail of a long log was read.
func TestParseLogDropsTruncatedBlock(t *testing.T) {
	lines, _ := ResultLines("n", sampleResult(900))
	if len(lines) < 3 {
		t.Fatalf("fixture too small: %d chunks", len(lines))
	}
	_, _, err := ParseLog(strings.Join(lines[1:], "\n"), "n")
	if !errors.Is(err, ErrNoResult) {
		t.Fatalf("got %v, a block missing its first chunk must be ignored", err)
	}
}

func TestRequestRoundTrip(t *testing.T) {
	req := &Request{
		Action:     corev1alpha1.FlareActionApply,
		Engine:     corev1alpha1.EngineOpenTofu,
		Terragrunt: &Terragrunt{Mode: corev1alpha1.TerragruntModeRunAll, ExcludedUnits: []string{"data/db"}},
		Scope:      corev1alpha1.FlareScopeAll,
		Repository: Repository{URL: "https://github.com/4sh/cinng-iac", Branch: "main", Path: "terragrunt"},
	}
	raw, err := req.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !got.RunAll() || got.Terragrunt.ExcludedUnits[0] != "data/db" {
		t.Fatalf("request did not round-trip: %+v", got)
	}
	got.Scope = corev1alpha1.FlareScopeUnit
	if got.RunAll() {
		t.Fatal("a unit-scoped Flare must not run the whole stack")
	}
}
