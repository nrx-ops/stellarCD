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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
	"github.com/nrx-ops/stellarCD/internal/runspec"
)

// maxOutputTail is how much of the engine output is carried back to the status.
const maxOutputTail = 4 << 10

// CaptureFunc runs a command whose stdout is data, not log.
type CaptureFunc func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error)

// Runner executes one run request inside a Flare's Job.
type Runner struct {
	Request *runspec.Request
	// Nonce tags the markers the controller trusts.
	Nonce string
	// Log is the pod log, where markers are written.
	Log io.Writer
	// Exec runs a command whose output belongs in the log.
	Exec CommandRunner
	// Capture runs a command whose stdout must stay out of the log.
	Capture CaptureFunc
	// Env is the environment handed to every child process.
	Env []string
	// WorkDir is the writable scratch directory; defaults to runspec.WorkDir.
	WorkDir string
	// CredentialsDir holds the mounted git credentials; defaults to
	// runspec.GitCredentialsDir. A missing directory means an anonymous clone.
	CredentialsDir string
	// Workspace fetches the repository; defaults to a git CLI checkout.
	Workspace WorkspaceProvider

	output cappedBuffer
}

// Run executes the request and returns its result. It never returns an
// error: every failure is a result the controller has to record.
func (r *Runner) Run(ctx context.Context) *runspec.Result {
	req := r.Request
	result := &runspec.Result{}
	lay := newLayout(req)

	r.step(corev1alpha1.FlareStepGitFetch, result)
	checkout, err := r.fetch(ctx)
	if err != nil {
		return r.fail(result, err)
	}
	defer func() { _ = r.workspace().Cleanup(checkout) }()
	result.Revision = checkout.Revision

	dir := checkout.ModuleDir
	if req.Scope == corev1alpha1.FlareScopeUnit {
		if dir, err = resolveModuleDir(checkout.ModuleDir, req.UnitPath); err != nil {
			return r.fail(result, fmt.Errorf("unit %q: %w", req.UnitPath, err))
		}
	}

	r.step(corev1alpha1.FlareStepInit, result)
	if err := r.checkVersions(ctx, dir, lay); err != nil {
		return r.fail(result, err)
	}
	if req.Action == corev1alpha1.FlareActionDestroy && lay.runAll {
		// Destroying a stack has to walk the dependency graph backwards, which
		// applying saved plans does not do. Refusing is safer than guessing.
		return r.fail(result, errors.New("destroying every unit at once is not supported; "+
			"destroy the units one at a time with scope Unit"))
	}
	if err := r.init(ctx, dir, lay); err != nil {
		return r.fail(result, err)
	}

	r.step(corev1alpha1.FlareStepPlan, result)
	if err := r.hooks(ctx, dir, req.Hooks.PrePlan); err != nil {
		return r.fail(result, err)
	}
	report, units, err := r.plan(ctx, dir, lay)
	if err != nil {
		return r.fail(result, err)
	}
	r.recordPlan(result, report, units)
	if err := r.hooks(ctx, dir, req.Hooks.PostPlan); err != nil {
		return r.fail(result, err)
	}

	if !req.Mutates() {
		result.Succeeded = !anyUnitFailed(units)
		result.Message = "Plan: " + DescribeSummary(result.PlanSummary)
		if !result.Succeeded {
			result.Message = fmt.Sprintf("Plan: %s; %s", DescribeSummary(result.PlanSummary), failedUnits(units))
		}
		return r.finish(result)
	}

	// RG-Run-all blocked by a failed unit: a whole-stack apply is refused
	// while any unit fails, so nothing is applied on top of a broken unit.
	if anyUnitFailed(units) {
		return r.fail(result, fmt.Errorf("Apply blocked: %s. Fix it before reconciling all units", failedUnits(units)))
	}

	// Destroy protection: every deletion or replacement must have been
	// confirmed, for this exact set of resources.
	if pending := UnconfirmedDestroys(report.Destroys, req.AllowedDestroy); len(pending) > 0 {
		if len(report.Destroys) > runspec.MaxPendingDestroy {
			return r.fail(result, fmt.Errorf("the plan destroys %d resources, more than the %d stellarCD "+
				"can hold for confirmation; split the change", len(report.Destroys), runspec.MaxPendingDestroy))
		}
		slices.Sort(report.Destroys)
		result.PendingDestroy = report.Destroys
		result.NeedsConfirmation = true
		result.Succeeded = true
		result.Message = fmt.Sprintf("Apply held: the plan destroys %d resource(s) and needs confirmation",
			len(report.Destroys))
		return r.finish(result)
	}

	r.step(corev1alpha1.FlareStepApply, result)
	if err := r.hooks(ctx, dir, req.Hooks.PreApply); err != nil {
		return r.fail(result, err)
	}
	applied, err := r.apply(ctx, dir, lay)
	if applied != nil {
		mergeUnitResults(result, applied)
	}
	if err != nil {
		return r.fail(result, err)
	}
	if err := r.hooks(ctx, dir, req.Hooks.PostApply); err != nil {
		return r.fail(result, err)
	}

	result.Applied = true
	result.Succeeded = true
	result.Message = "Applied: " + DescribeSummary(result.PlanSummary)
	return r.finish(result)
}

// layout is how engine commands are invoked: the bare engine, Terragrunt on a
// single unit, or Terragrunt over every unit below the path.
type layout struct {
	binary     string
	terragrunt bool
	runAll     bool
	excluded   []string
}

func newLayout(req *runspec.Request) layout {
	l := layout{binary: engineBinary(req.Engine)}
	if req.Terragrunt != nil {
		l.terragrunt = true
		l.runAll = req.RunAll()
		if l.runAll {
			l.excluded = req.Terragrunt.ExcludedUnits
		}
	}
	return l
}

func engineBinary(engine corev1alpha1.Engine) string {
	if engine == corev1alpha1.EngineOpenTofu {
		return "tofu"
	}
	return "terraform"
}

// command wraps engine arguments for the layout. tgFlags are Terragrunt flags,
// ignored without Terragrunt.
func (l layout) command(engineArgs []string, tgFlags ...string) (string, []string) {
	if !l.terragrunt {
		return l.binary, engineArgs
	}
	args := []string{"run"}
	if l.runAll {
		args = append(args, "--all")
	}
	// Terragrunt 1.x drives OpenTofu unless told otherwise.
	args = append(args, "--tf-path", l.binary)
	for _, unit := range l.excluded {
		// Several negated filters intersect, which is what an exclusion list
		// needs. Verified against Terragrunt 1.1.6.
		args = append(args, "--filter", "!{./"+cleanUnit(unit)+"}")
	}
	args = append(args, tgFlags...)
	args = append(args, "--")
	return "terragrunt", append(args, engineArgs...)
}

func cleanUnit(unit string) string {
	return strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(unit)), "/")
}

func (r *Runner) fetch(ctx context.Context) (*Checkout, error) {
	creds, err := readSecretDir(r.credentialsDir())
	if err != nil {
		return nil, err
	}
	repo := r.Request.Repository
	return r.workspace().Prepare(ctx, CheckoutRequest{
		Key:         "src",
		URL:         repo.URL,
		Branch:      repo.Branch,
		Revision:    repo.Revision,
		Path:        repo.Path,
		Credentials: creds,
	})
}

// checkVersions fails the run before init when the image does not ship the
// requested versions. The specification is explicit that a missing version
// fails the Flare rather than silently running another one.
func (r *Runner) checkVersions(ctx context.Context, dir string, lay layout) error {
	if want := r.Request.EngineVersion; want != "" {
		out, err := r.capture()(ctx, dir, r.Env, lay.binary, "version", "-json")
		if err != nil {
			return fmt.Errorf("failed to read the %s version: %w", lay.binary, err)
		}
		var v struct {
			Version string `json:"terraform_version"`
		}
		if err := json.Unmarshal(out, &v); err != nil {
			return fmt.Errorf("failed to parse the %s version: %w", lay.binary, err)
		}
		if normaliseVersion(v.Version) != normaliseVersion(want) {
			return fmt.Errorf("engine version %s was requested but the runner image ships %s %s",
				want, lay.binary, v.Version)
		}
	}
	if r.Request.Terragrunt != nil && r.Request.Terragrunt.Version != "" {
		out, err := r.capture()(ctx, dir, r.Env, "terragrunt", "--version")
		if err != nil {
			return fmt.Errorf("failed to read the terragrunt version: %w", err)
		}
		fields := strings.Fields(string(out))
		got := ""
		if len(fields) > 0 {
			got = fields[len(fields)-1]
		}
		if normaliseVersion(got) != normaliseVersion(r.Request.Terragrunt.Version) {
			return fmt.Errorf("terragrunt version %s was requested but the runner image ships %s",
				r.Request.Terragrunt.Version, got)
		}
	}
	return nil
}

func normaliseVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

func (r *Runner) init(ctx context.Context, dir string, lay layout) error {
	if lay.runAll {
		// A stack-wide "init" fails as a whole as soon as one unit is broken,
		// which would hide every healthy unit's result. Terragrunt initialises
		// each unit on its own during the plan instead, and reports failures
		// per unit; the backend configuration reaches those inits through
		// TF_CLI_ARGS_init (see childEnv).
		return nil
	}
	args := engineArgs("init")
	if r.Request.BackendConfig {
		args = append(args, "-backend-config="+runspec.BackendConfigFile)
	}
	if err := r.exec(ctx, dir, lay, args); err != nil {
		return err
	}
	ws := r.Request.Workspace
	if ws != "" && !lay.terragrunt {
		// -or-create keeps a first run from failing on a fresh backend.
		return r.exec(ctx, dir, lay, []string{"workspace", "select", "-or-create=true", ws})
	}
	return nil
}

// engineArgs builds a non-interactive engine command line: no prompt, and no
// colour codes in the log the controller parses.
func engineArgs(command string, extra ...string) []string {
	return append([]string{command, "-input=false", "-no-color"}, extra...)
}

// plan runs the plan and reads it back as JSON. In RunAll mode Terragrunt
// writes one saved plan and one JSON plan per unit, plus a run report that says
// which units failed and which were skipped because a dependency failed.
func (r *Runner) plan(ctx context.Context, dir string, lay layout) (*PlanReport, []corev1alpha1.FlareUnitResult, error) {
	req := r.Request
	args := engineArgs("plan",
		fmt.Sprintf("-lock=%t", req.Lock),
		fmt.Sprintf("-parallelism=%d", parallelism(req)),
	)
	refreshOnly := req.Action == corev1alpha1.FlareActionRefresh
	switch {
	case refreshOnly:
		args = append(args, "-refresh-only")
	default:
		args = append(args, fmt.Sprintf("-refresh=%t", req.RefreshBeforePlan))
	}
	if req.Action == corev1alpha1.FlareActionDestroy {
		args = append(args, "-destroy")
	}

	if !lay.runAll {
		planFile := r.planFile()
		if err := os.MkdirAll(filepath.Dir(planFile), 0o700); err != nil {
			return nil, nil, fmt.Errorf("failed to create the plan directory: %w", err)
		}
		args = append(args, "-out="+planFile)
		args = append(args, varArgs(req.Variables)...)
		if err := r.exec(ctx, dir, lay, args); err != nil {
			return nil, nil, err
		}
		name, showArgs := lay.command([]string{"show", "-json", "-no-color", planFile})
		data, err := r.capture()(ctx, dir, r.Env, name, showArgs...)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read the plan back as JSON: %w", err)
		}
		report, err := ParsePlan(data, "", refreshOnly)
		return report, nil, err
	}

	args = append(args, varArgs(req.Variables)...)
	reportFile := filepath.Join(r.workDir(), "plan-report.json")
	runErr := r.exec(ctx, dir, lay, args,
		"--out-dir", r.planDir(), "--json-out-dir", r.jsonDir(),
		"--report-file", reportFile, "--report-format", "json")

	units, err := r.collectUnits(dir, reportFile, lay)
	if err != nil {
		if runErr != nil {
			return nil, nil, runErr
		}
		return nil, nil, err
	}
	// A failed unit makes Terragrunt exit non-zero; that is reported per unit,
	// not as a failure of the whole step.
	report := &PlanReport{}
	for i := range units {
		unit := &units[i]
		if unit.Result != corev1alpha1.UnitResultSucceeded {
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.jsonDir(), filepath.FromSlash(unit.Path), "tfplan.json"))
		if err != nil {
			unit.Result = corev1alpha1.UnitResultFailed
			unit.Message = "the unit planned but wrote no JSON plan"
			continue
		}
		unitReport, err := ParsePlan(data, unit.Path, refreshOnly)
		if err != nil {
			unit.Result = corev1alpha1.UnitResultFailed
			unit.Message = err.Error()
			continue
		}
		summary := unitReport.Summary
		unit.PlanSummary = &summary
		report.Summary.Add(&summary)
		report.Changes = append(report.Changes, unitReport.Changes...)
		report.Destroys = append(report.Destroys, unitReport.Destroys...)
	}
	return report, units, nil
}

// apply applies the saved plan, so what runs is exactly what was checked by
// the destroy protection: nothing is re-planned in between.
func (r *Runner) apply(ctx context.Context, dir string, lay layout) ([]corev1alpha1.FlareUnitResult, error) {
	req := r.Request
	args := engineArgs("apply",
		fmt.Sprintf("-lock=%t", req.Lock),
		fmt.Sprintf("-parallelism=%d", parallelism(req)),
	)
	if !lay.runAll {
		return nil, r.exec(ctx, dir, lay, append(args, r.planFile()))
	}

	reportFile := filepath.Join(r.workDir(), "apply-report.json")
	runErr := r.exec(ctx, dir, lay, args,
		"--out-dir", r.planDir(), "--report-file", reportFile, "--report-format", "json")
	units, err := r.collectUnits(dir, reportFile, lay)
	if err != nil {
		units = nil
	}
	if runErr != nil {
		return units, fmt.Errorf("apply failed: %s", failedUnitsOr(units, runErr.Error()))
	}
	return units, nil
}

func (r *Runner) hooks(ctx context.Context, dir string, hooks []string) error {
	for _, hook := range hooks {
		out, _, err := r.exec0(ctx, dir, "/bin/sh", "-c", hook)
		_, _ = r.output.Write([]byte(out))
		if err != nil {
			return fmt.Errorf("hook %q failed: %w", hook, err)
		}
	}
	return nil
}

// exec runs an engine command through the layout and keeps its output tail.
func (r *Runner) exec(ctx context.Context, dir string, lay layout, engineArgs []string, tgFlags ...string) error {
	name, args := lay.command(engineArgs, tgFlags...)
	out, _, err := r.exec0(ctx, dir, name, args...)
	_, _ = r.output.Write([]byte(out))
	if err != nil {
		return fmt.Errorf("%s %s failed: %w", name, engineArgs[0], err)
	}
	return nil
}

func (r *Runner) exec0(ctx context.Context, dir, name string, args ...string) (string, int, error) {
	return r.Exec(ctx, dir, r.childEnv(), name, args...)
}

// childEnv is the environment for engine commands. In RunAll mode the backend
// configuration rides on TF_CLI_ARGS_init, the one way to reach the per-unit
// inits Terragrunt runs on its own.
func (r *Runner) childEnv() []string {
	if !r.Request.RunAll() || !r.Request.BackendConfig {
		return r.Env
	}
	return append(slices.Clone(r.Env), "TF_CLI_ARGS_init=-backend-config="+runspec.BackendConfigFile)
}

// recordPlan copies the plan report onto the result.
func (r *Runner) recordPlan(result *runspec.Result, report *PlanReport, units []corev1alpha1.FlareUnitResult) {
	summary := report.Summary
	result.PlanSummary = &summary
	changes := report.Changes
	if len(changes) > runspec.MaxResourceChanges {
		changes = changes[:runspec.MaxResourceChanges]
		result.ChangesTruncated = true
	}
	result.Changes = changes
	result.Units = units
}

// mergeUnitResults folds the apply report over the plan's unit results: a unit
// that planned fine can still fail to apply.
func mergeUnitResults(result *runspec.Result, applied []corev1alpha1.FlareUnitResult) {
	byPath := make(map[string]corev1alpha1.FlareUnitResult, len(applied))
	for _, u := range applied {
		byPath[u.Path] = u
	}
	for i := range result.Units {
		if a, ok := byPath[result.Units[i].Path]; ok && a.Result != corev1alpha1.UnitResultSucceeded {
			result.Units[i].Result = a.Result
			result.Units[i].Message = a.Message
		}
	}
}

func (r *Runner) step(step corev1alpha1.FlareStep, result *runspec.Result) {
	result.Step = step
	_, _ = fmt.Fprintln(r.log(), runspec.StepLine(r.Nonce, step))
}

func (r *Runner) fail(result *runspec.Result, err error) *runspec.Result {
	result.Succeeded = false
	result.Message = err.Error()
	// "tofu init failed: exited with status 1" says where, not why. The
	// engine's own first error line is what a person needs on one line.
	if errs, _ := diagnostics(r.output.String()); len(errs) > 0 {
		result.Message = err.Error() + ": " + errs[0]
	}
	result.Errors = appendCapped(result.Errors, err.Error())
	return r.finish(result)
}

// finish attaches the output tail and the scraped diagnostics, then writes
// the result block to the log.
func (r *Runner) finish(result *runspec.Result) *runspec.Result {
	output := r.output.String()
	result.Output = tail(output, maxOutputTail)
	errs, warns := diagnostics(output)
	for _, e := range errs {
		result.Errors = appendCapped(result.Errors, e)
	}
	result.Warnings = warns

	r.step(corev1alpha1.FlareStepReport, &runspec.Result{})
	lines, err := runspec.ResultLines(r.Nonce, result)
	if err != nil {
		// Encoding a plain struct cannot fail in practice; if it ever does,
		// the controller sees no result and fails the Flare on the exit code.
		_, _ = fmt.Fprintln(r.log(), "failed to encode the result:", err)
		return result
	}
	for _, line := range lines {
		_, _ = fmt.Fprintln(r.log(), line)
	}
	return result
}

// reportEntry is one line of Terragrunt's JSON run report.
type reportEntry struct {
	Name   string `json:"Name"`
	Result string `json:"Result"`
	Reason string `json:"Reason"`
	Cause  string `json:"Cause"`
}

// collectUnits merges the units found on disk with Terragrunt's run report.
// Excluded units do not appear in the report at all, which is why the list
// starts from the directory tree rather than from the report.
func (r *Runner) collectUnits(dir, reportFile string, lay layout) ([]corev1alpha1.FlareUnitResult, error) {
	onDisk, err := discoverUnits(dir)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(reportFile)
	if err != nil {
		return nil, fmt.Errorf("terragrunt wrote no run report: %w", err)
	}
	var entries []reportEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("failed to parse the terragrunt run report: %w", err)
	}
	reported := make(map[string]reportEntry, len(entries))
	for _, e := range entries {
		reported[cleanUnit(e.Name)] = e
	}
	excluded := make(map[string]bool, len(lay.excluded))
	for _, u := range lay.excluded {
		excluded[cleanUnit(u)] = true
	}

	units := make([]corev1alpha1.FlareUnitResult, 0, len(onDisk))
	for _, unit := range onDisk {
		res := corev1alpha1.FlareUnitResult{Path: unit}
		entry, ok := reported[unit]
		switch {
		case excluded[unit]:
			res.Result = corev1alpha1.UnitResultExcluded
			res.Message = "listed in terragrunt.excludedUnits"
		case !ok:
			res.Result = corev1alpha1.UnitResultSkipped
			res.Message = "terragrunt did not run this unit"
		default:
			res.Result, res.Message = unitOutcome(entry)
		}
		units = append(units, res)
	}
	return units, nil
}

// unitOutcome maps a report entry onto a unit result. "early exit" is how
// Terragrunt marks a unit it skipped because a dependency failed.
func unitOutcome(e reportEntry) (corev1alpha1.UnitResult, string) {
	switch e.Result {
	case "succeeded":
		return corev1alpha1.UnitResultSucceeded, ""
	case "early exit":
		return corev1alpha1.UnitResultSkipped, strings.TrimSpace("skipped: " + e.Reason + " " + e.Cause)
	case "excluded":
		return corev1alpha1.UnitResultExcluded, e.Reason
	default:
		msg := strings.TrimSpace(e.Reason + ": " + e.Cause)
		return corev1alpha1.UnitResultFailed, strings.TrimPrefix(msg, ": ")
	}
}

// discoverUnits lists the unit directories below dir, as slash paths relative
// to it, skipping Terragrunt's own cache.
func discoverUnits(dir string) ([]string, error) {
	var units []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != dir && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if d.IsDir() || d.Name() != "terragrunt.hcl" {
			return nil
		}
		rel, err := filepath.Rel(dir, filepath.Dir(p))
		if err != nil {
			return err
		}
		units = append(units, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list the units: %w", err)
	}
	slices.Sort(units)
	return units, nil
}

func anyUnitFailed(units []corev1alpha1.FlareUnitResult) bool {
	return slices.ContainsFunc(units, func(u corev1alpha1.FlareUnitResult) bool {
		return u.Result == corev1alpha1.UnitResultFailed || u.Result == corev1alpha1.UnitResultSkipped
	})
}

func failedUnits(units []corev1alpha1.FlareUnitResult) string {
	var failed, skipped []string
	for _, u := range units {
		switch u.Result {
		case corev1alpha1.UnitResultFailed:
			failed = append(failed, u.Path)
		case corev1alpha1.UnitResultSkipped:
			skipped = append(skipped, u.Path)
		}
	}
	msg := fmt.Sprintf("%d unit(s) failed (%s)", len(failed), strings.Join(failed, ", "))
	if len(skipped) > 0 {
		msg += fmt.Sprintf(", %d skipped behind them", len(skipped))
	}
	return msg
}

func failedUnitsOr(units []corev1alpha1.FlareUnitResult, fallback string) string {
	if anyUnitFailed(units) {
		return failedUnits(units)
	}
	return fallback
}

// varArgs renders -var arguments in a stable order.
func varArgs(vars map[string]string) []string {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	args := make([]string, 0, len(keys))
	for _, k := range keys {
		args = append(args, fmt.Sprintf("-var=%s=%s", k, vars[k]))
	}
	return args
}

func parallelism(req *runspec.Request) int32 {
	if req.Parallelism <= 0 {
		return corev1alpha1.DefaultFlareParallelism
	}
	return req.Parallelism
}

// readSecretDir loads a mounted Secret volume into the map the workspace
// expects. Kubernetes projects each key as a file, plus hidden ..data
// bookkeeping entries that are skipped.
func readSecretDir(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the git credentials: %w", err)
	}
	creds := map[string][]byte{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name())) // #nosec G304 -- fixed mount point
		if err != nil {
			return nil, fmt.Errorf("failed to read git credential %q: %w", e.Name(), err)
		}
		creds[e.Name()] = data
	}
	return creds, nil
}

func (r *Runner) workDir() string {
	if r.WorkDir != "" {
		return r.WorkDir
	}
	return runspec.WorkDir
}

func (r *Runner) credentialsDir() string {
	if r.CredentialsDir != "" {
		return r.CredentialsDir
	}
	return runspec.GitCredentialsDir
}

func (r *Runner) planDir() string  { return filepath.Join(r.workDir(), "plans") }
func (r *Runner) jsonDir() string  { return filepath.Join(r.workDir(), "json") }
func (r *Runner) planFile() string { return filepath.Join(r.planDir(), "tfplan") }

func (r *Runner) workspace() WorkspaceProvider {
	if r.Workspace == nil {
		r.Workspace = &GitWorkspace{Root: r.workDir(), Runner: r.Exec}
	}
	return r.Workspace
}

func (r *Runner) capture() CaptureFunc {
	return r.Capture
}

func (r *Runner) log() io.Writer {
	if r.Log == nil {
		return io.Discard
	}
	return r.Log
}
