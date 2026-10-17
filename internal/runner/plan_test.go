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
	"os"
	"slices"
	"testing"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
)

// The fixtures are real "terraform show -json" outputs, produced by Terraform
// 1.16 against terraform_data resources, not hand-written approximations.
func TestParsePlanFixtures(t *testing.T) {
	tests := []struct {
		fixture  string
		unit     string
		want     corev1alpha1.PlanSummary
		destroys []string
	}{
		{
			fixture: "create.json",
			want:    corev1alpha1.PlanSummary{ToCreate: 3},
		},
		{
			fixture:  "mixed.json",
			want:     corev1alpha1.PlanSummary{ToUpdate: 1, ToDestroy: 1, ToReplace: 1},
			destroys: []string{"terraform_data.doomed[0]", "terraform_data.replaced"},
		},
		{
			fixture: "noop.json",
			want:    corev1alpha1.PlanSummary{},
		},
		{
			fixture:  "mixed.json",
			unit:     "apps/x/ns",
			want:     corev1alpha1.PlanSummary{ToUpdate: 1, ToDestroy: 1, ToReplace: 1},
			destroys: []string{"apps/x/ns::terraform_data.doomed[0]", "apps/x/ns::terraform_data.replaced"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.fixture+"/"+tc.unit, func(t *testing.T) {
			data, err := os.ReadFile("testdata/" + tc.fixture)
			if err != nil {
				t.Fatal(err)
			}
			report, err := ParsePlan(data, tc.unit, false)
			if err != nil {
				t.Fatal(err)
			}
			if report.Summary != tc.want {
				t.Fatalf("got %+v, want %+v", report.Summary, tc.want)
			}
			slices.Sort(report.Destroys)
			if !slices.Equal(report.Destroys, tc.destroys) {
				t.Fatalf("got destroys %v, want %v", report.Destroys, tc.destroys)
			}
			for _, c := range report.Changes {
				if c.Unit != tc.unit {
					t.Fatalf("change %s carries unit %q, want %q", c.Address, c.Unit, tc.unit)
				}
			}
		})
	}
}

// TestParsePlanNeverDecodesValues guards the property the parser is built on:
// attribute values stay out of everything it returns.
func TestParsePlanNeverDecodesValues(t *testing.T) {
	data, err := os.ReadFile("testdata/mixed.json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := ParsePlan(data, "", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range report.Changes {
		// The fixture's payload values are "a" and "b"; addresses never are.
		if c.Address == "a" || c.Address == "b" {
			t.Fatalf("a value leaked into the change list: %+v", c)
		}
	}
}

func TestParsePlanRefreshOnly(t *testing.T) {
	doc := []byte(`{"resource_changes":[],"resource_drift":[
		{"address":"aws_s3_bucket.logs","mode":"managed","change":{"actions":["update"]}},
		{"address":"aws_iam_role.gone","mode":"managed","change":{"actions":["delete"]}}]}`)
	report, err := ParsePlan(doc, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.ToUpdate != 1 || report.Summary.ToDestroy != 1 {
		t.Fatalf("got %+v, a refresh-only plan reports out-of-band drift", report.Summary)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		actions []string
		want    corev1alpha1.ChangeAction
		ok      bool
	}{
		{[]string{"create"}, corev1alpha1.ChangeActionCreate, true},
		{[]string{"update"}, corev1alpha1.ChangeActionUpdate, true},
		{[]string{"delete"}, corev1alpha1.ChangeActionDelete, true},
		{[]string{"delete", "create"}, corev1alpha1.ChangeActionReplace, true},
		// create_before_destroy still destroys the old object.
		{[]string{"create", "delete"}, corev1alpha1.ChangeActionReplace, true},
		{[]string{"no-op"}, "", false},
		{[]string{"read"}, "", false},
		// "removed" blocks forget a resource without destroying it.
		{[]string{"forget"}, "", false},
	}
	for _, tc := range tests {
		got, ok := classify(tc.actions)
		if got != tc.want || ok != tc.ok {
			t.Errorf("classify(%v) = %q, %v; want %q, %v", tc.actions, got, ok, tc.want, tc.ok)
		}
	}
}

func TestUnconfirmedDestroys(t *testing.T) {
	tests := []struct {
		name     string
		destroys []string
		allowed  []string
		want     []string
	}{
		{name: "nothing destroyed", want: nil},
		{name: "nothing confirmed", destroys: []string{"a"}, want: []string{"a"}},
		{name: "fully confirmed", destroys: []string{"a", "b"}, allowed: []string{"a", "b"}, want: nil},
		// The plan moved on after the user confirmed: the new destruction is held.
		{name: "new destruction", destroys: []string{"a", "c"}, allowed: []string{"a", "b"}, want: []string{"c"}},
		// Same address in two units: a confirmation for one does not cover the other.
		{
			name:     "unit qualified",
			destroys: []string{"x::aws_s3_bucket.b", "y::aws_s3_bucket.b"},
			allowed:  []string{"x::aws_s3_bucket.b"},
			want:     []string{"y::aws_s3_bucket.b"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := UnconfirmedDestroys(tc.destroys, tc.allowed)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
