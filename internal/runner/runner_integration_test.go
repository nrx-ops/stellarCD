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
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
	"github.com/nrx-ops/stellarCD/internal/runspec"
)

// These tests drive the real engine binaries against a real git repository.
// They are what proves the command lines, the saved-plan apply and the
// Terragrunt report handling, which a stubbed CommandRunner cannot. They skip
// when the binaries are not on PATH, so CI without them stays green.

func requireBinaries(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s is not on PATH", name)
		}
	}
}

// gitRepo is a throwaway repository the runner clones over file://.
type gitRepo struct {
	t   *testing.T
	dir string
}

func newGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	r := &gitRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	r.git("config", "user.email", "test@stellarcd.invalid")
	r.git("config", "user.name", "stellarcd test")
	return r
}

func (r *gitRepo) git(args ...string) {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (r *gitRepo) write(rel, content string) {
	r.t.Helper()
	p := filepath.Join(r.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *gitRepo) commit(msg string) {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "-m", msg)
}

func (r *gitRepo) url() string { return "file://" + r.dir }

// run executes one request the way cmd/runner does, and checks the result also
// survives the trip through the log.
func run(t *testing.T, req *runspec.Request) *runspec.Result {
	t.Helper()
	var log bytes.Buffer
	shell := &Shell{Log: &log}
	r := &Runner{
		Request:        req,
		Nonce:          "test-nonce",
		Log:            &log,
		Exec:           shell.Run,
		Capture:        shell.Capture,
		Env:            ChildEnv(os.Environ()),
		WorkDir:        t.TempDir(),
		CredentialsDir: filepath.Join(t.TempDir(), "absent"),
	}
	result := r.Run(context.Background())

	_, fromLog, err := runspec.ParseLog(log.String(), "test-nonce")
	if err != nil {
		t.Fatalf("no result in the log: %v\n%s", err, log.String())
	}
	if fromLog.Succeeded != result.Succeeded || fromLog.NeedsConfirmation != result.NeedsConfirmation {
		t.Fatalf("log result %+v differs from returned %+v", fromLog, result)
	}
	if !result.Succeeded {
		t.Logf("runner log:\n%s", log.String())
	}
	return result
}

const moduleV1 = `
terraform {
  backend "local" { path = "STATE" }
}
variable "keep" { default = true }
resource "terraform_data" "kept" { input = "a" }
resource "terraform_data" "doomed" { count = var.keep ? 1 : 0 }
`

func TestRunnerTerraformDestroyProtection(t *testing.T) {
	requireBinaries(t, "git", "terraform")

	state := filepath.Join(t.TempDir(), "terraform.tfstate")
	repo := newGitRepo(t)
	repo.write("infra/main.tf", strings.ReplaceAll(moduleV1, "STATE", state))
	repo.commit("v1")

	base := runspec.Request{
		Engine:     corev1alpha1.EngineTerraform,
		Scope:      corev1alpha1.FlareScopeAll,
		Repository: runspec.Repository{URL: repo.url(), Branch: defaultBranch, Path: "infra"},
		Lock:       true,
	}

	// 1. A plan reports the drift and changes nothing.
	plan := base
	plan.Action = corev1alpha1.FlareActionPlan
	res := run(t, &plan)
	if !res.Succeeded || res.PlanSummary.ToCreate != 2 || res.Applied {
		t.Fatalf("plan: %+v", res)
	}
	if len(res.Revision) != 40 {
		t.Fatalf("plan did not report the commit it ran: %q", res.Revision)
	}

	// 2. An apply that only creates goes straight through.
	apply := base
	apply.Action = corev1alpha1.FlareActionApply
	if res = run(t, &apply); !res.Succeeded || !res.Applied {
		t.Fatalf("first apply: %+v", res)
	}

	// 3. The code now drops a resource: the apply is held for confirmation and
	//    real infrastructure is untouched.
	repo.write("infra/main.tf", strings.ReplaceAll(
		strings.ReplaceAll(moduleV1, "STATE", state), "default = true", "default = false"))
	repo.commit("drop doomed")

	res = run(t, &apply)
	if !res.Succeeded || res.Applied || !res.NeedsConfirmation {
		t.Fatalf("destroying apply was not held: %+v", res)
	}
	if !slices.Equal(res.PendingDestroy, []string{"terraform_data.doomed[0]"}) {
		t.Fatalf("got pending destroy %v", res.PendingDestroy)
	}
	if res.Step != corev1alpha1.FlareStepPlan {
		t.Fatalf("a held apply must stop at the plan step, got %q", res.Step)
	}
	stateData, _ := os.ReadFile(state)
	if !strings.Contains(string(stateData), "doomed") {
		t.Fatal("the held apply destroyed the resource anyway")
	}

	// 4. Once confirmed for exactly that resource, it applies.
	apply.AllowedDestroy = res.PendingDestroy
	if res = run(t, &apply); !res.Succeeded || !res.Applied || res.NeedsConfirmation {
		t.Fatalf("confirmed apply: %+v", res)
	}
	stateData, _ = os.ReadFile(state)
	if strings.Contains(string(stateData), "doomed") {
		t.Fatal("the confirmed apply did not destroy the resource")
	}

	// 5. A plan of the converged code is clean.
	if res = run(t, &plan); res.PlanSummary.HasChanges() {
		t.Fatalf("converged plan still has changes: %+v", res.PlanSummary)
	}
}

func TestRunnerEngineVersionMismatch(t *testing.T) {
	requireBinaries(t, "git", "terraform")

	repo := newGitRepo(t)
	repo.write("main.tf", `resource "terraform_data" "x" {}`)
	repo.commit("v1")

	res := run(t, &runspec.Request{
		Action:        corev1alpha1.FlareActionPlan,
		Engine:        corev1alpha1.EngineTerraform,
		EngineVersion: "0.12.0",
		Scope:         corev1alpha1.FlareScopeAll,
		Repository:    runspec.Repository{URL: repo.url(), Branch: defaultBranch},
	})
	if res.Succeeded || res.Step != corev1alpha1.FlareStepInit ||
		!strings.Contains(res.Message, "0.12.0 was requested") {
		t.Fatalf("a missing engine version must fail before init: %+v", res)
	}
}

func TestRunnerTerragruntRunAll(t *testing.T) {
	requireBinaries(t, "git", "terraform", "terragrunt")

	stateDir := t.TempDir()
	repo := newGitRepo(t)
	// No backend block here: each unit's remote_state generates one.
	repo.write("modules/data/main.tf", `
variable "name" { type = string }
resource "terraform_data" "x" { input = var.name }
output "name" { value = var.name }
`)
	unit := func(name, input string) string {
		return `terraform { source = "../../../modules/data" }
remote_state {
  backend = "local"
  config  = { path = "` + stateDir + `/` + name + `.tfstate" }
  generate = { path = "backend.tf", if_exists = "overwrite_terragrunt" }
}
inputs = { name = ` + input + ` }
`
	}
	repo.write("stack/apps/web/terragrunt.hcl", unit("web", `"web"`))
	repo.write("stack/apps/api/terragrunt.hcl", unit("api", `"api"`))
	repo.write("stack/data/db/terragrunt.hcl", unit("db", `"db"`))
	repo.commit("stack")

	req := &runspec.Request{
		Action:     corev1alpha1.FlareActionPlan,
		Engine:     corev1alpha1.EngineTerraform,
		Terragrunt: &runspec.Terragrunt{Mode: corev1alpha1.TerragruntModeRunAll, ExcludedUnits: []string{"data/db"}},
		Scope:      corev1alpha1.FlareScopeAll,
		Repository: runspec.Repository{URL: repo.url(), Branch: defaultBranch, Path: "stack"},
		Lock:       true,
	}

	// A healthy run-all: per-unit drift, the excluded unit reported as such.
	res := run(t, req)
	if !res.Succeeded || res.PlanSummary.ToCreate != 2 {
		t.Fatalf("run-all plan: %+v", res)
	}
	results := map[string]corev1alpha1.UnitResult{}
	for _, u := range res.Units {
		results[u.Path] = u.Result
	}
	want := map[string]corev1alpha1.UnitResult{
		"apps/api": corev1alpha1.UnitResultSucceeded,
		"apps/web": corev1alpha1.UnitResultSucceeded,
		"data/db":  corev1alpha1.UnitResultExcluded,
	}
	for path, r := range want {
		if results[path] != r {
			t.Fatalf("unit %s: got %q, want %q (all: %v)", path, results[path], r, results)
		}
	}
	for _, c := range res.Changes {
		if c.Unit == "" {
			t.Fatalf("run-all change %s is not attributed to a unit", c.Address)
		}
	}

	// Apply the healthy stack from its saved plans.
	req.Action = corev1alpha1.FlareActionApply
	if res = run(t, req); !res.Succeeded || !res.Applied {
		t.Fatalf("run-all apply: %+v", res)
	}

	// Break one unit: the plan reports it, and a whole-stack apply is refused.
	// An evaluation error, the common kind: the unit fails, its siblings run.
	repo.write("stack/apps/api/terragrunt.hcl", unit("api", "local.missing"))
	repo.commit("break api")
	res = run(t, req)
	if res.Succeeded || res.Applied || !strings.Contains(res.Message, "Apply blocked") {
		t.Fatalf("an apply over a failed unit must be refused: %+v", res)
	}
	got := map[string]corev1alpha1.UnitResult{}
	for _, u := range res.Units {
		got[u.Path] = u.Result
	}
	if got["apps/api"] != corev1alpha1.UnitResultFailed || got["apps/web"] != corev1alpha1.UnitResultSucceeded {
		t.Fatalf("a broken unit must not hide its siblings' results: %v", got)
	}

	// The healthy unit can still be applied on its own.
	unitReq := *req
	unitReq.Scope = corev1alpha1.FlareScopeUnit
	unitReq.UnitPath = "apps/web"
	if res = run(t, &unitReq); !res.Succeeded {
		t.Fatalf("a unit-scoped apply on a healthy unit must work: %+v", res)
	}
}
