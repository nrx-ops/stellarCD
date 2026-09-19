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

// Package shared holds the execution primitives the stellarCD controllers build
// on: the Terraform/OpenTofu/Terragrunt wrapper and the per-state lock that
// keeps concurrent runs from corrupting remote state.
package shared

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Engine names the binary invoked by the executor.
type Engine string

const (
	EngineTerraform  Engine = "Terraform"
	EngineOpenTofu   Engine = "OpenTofu"
	EngineTerragrunt Engine = "Terragrunt"
)

// Binary maps an engine onto the executable looked up in PATH.
func (e Engine) Binary() string {
	switch e {
	case EngineOpenTofu:
		return "tofu"
	case EngineTerragrunt:
		return "terragrunt"
	case EngineTerraform:
		return "terraform"
	default:
		return "terraform"
	}
}

// Action is the operation to perform against a module.
type Action string

const (
	ActionPlan    Action = "Plan"
	ActionApply   Action = "Apply"
	ActionDestroy Action = "Destroy"
	ActionRefresh Action = "Refresh"
)

// exitCodeChangesPresent is what -detailed-exitcode returns when the plan is
// non-empty. It is a successful outcome, not a failure.
const exitCodeChangesPresent = 2

// maxOutputBytes bounds what the executor keeps in memory and hands back for
// the status. Engine output on a large module runs to megabytes; a CRD status
// that big is rejected by etcd.
const maxOutputBytes = 64 << 10

// redactedPlaceholder replaces every secret value found in captured output.
const redactedPlaceholder = "***REDACTED***"

// RunRequest is a single engine invocation. It is deliberately a plain struct
// with no Kubernetes types so the executor stays unit-testable without a
// cluster and reusable outside the reconcile loop.
type RunRequest struct {
	// Engine selects the binary.
	Engine Engine
	// Action selects the operation.
	Action Action
	// WorkDir is the module directory. It must already contain the checkout.
	WorkDir string
	// Workspace is selected before the operation; empty skips the selection.
	Workspace string
	// Parallelism is passed as -parallelism.
	Parallelism int32
	// Lock enables engine state locking. Turning it off is only safe for
	// read-only modules.
	Lock bool
	// RefreshBeforePlan controls -refresh on plan.
	RefreshBeforePlan bool
	// Variables become -var key=value arguments. Sensitive inputs belong in
	// Env, not here: arguments are visible in the process table.
	Variables map[string]string
	// Env holds the resolved environment for the process, including the
	// credentials projected from Kubernetes Secrets.
	Env map[string]string
	// PlanFile, when set, is written by Plan (-out) and consumed by Apply.
	PlanFile string
	// Hooks are shell commands run around the operation.
	Hooks Hooks
	// SkipInit skips the init step, e.g. when the working directory was
	// initialised by a previous run in the same checkout.
	SkipInit bool
}

// Hooks are the shell commands run around the engine invocation.
type Hooks struct {
	Pre  []string
	Post []string
}

// ResourceCounts is the resource delta parsed out of the engine output.
type ResourceCounts struct {
	Created   int32
	Updated   int32
	Deleted   int32
	Unchanged int32
}

// RunResult is the outcome of a RunRequest.
type RunResult struct {
	// Output is the combined, redacted and truncated engine output.
	Output string
	// ExitCode is the exit status of the final command.
	ExitCode int
	// ChangesPresent reports whether the plan was non-empty. For a Refresh it
	// means the real infrastructure has drifted from the tracked revision.
	ChangesPresent bool
	// Counts is the resource delta.
	Counts ResourceCounts
	// Errors and Warnings are the diagnostics scraped from the output.
	Errors   []string
	Warnings []string
	// Duration is the wall time of the whole request.
	Duration time.Duration
}

// Executor runs one RunRequest to completion. The interface exists so the Flare
// controller can be tested without a terraform binary on PATH.
type Executor interface {
	Run(ctx context.Context, req RunRequest) (*RunResult, error)
}

// CommandRunner executes one command and returns its combined output and exit
// code. It is the seam the fake executor replaces in tests.
type CommandRunner func(ctx context.Context, dir string, env []string, name string, args ...string) (string, int, error)

// CommandExecutor is the production Executor: it shells out to the engine
// binary with os/exec, honouring the context for cancellation and timeouts.
type CommandExecutor struct {
	// Runner defaults to execCommand when nil.
	Runner CommandRunner
	// Shell runs the hook commands; defaults to "/bin/sh".
	Shell string
}

// NewCommandExecutor returns an Executor backed by os/exec.
func NewCommandExecutor() *CommandExecutor {
	return &CommandExecutor{Runner: execCommand, Shell: "/bin/sh"}
}

// Run executes the request: optional init, optional workspace selection, the
// pre hooks, the operation itself and the post hooks. It stops at the first
// failing step.
func (e *CommandExecutor) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	start := time.Now()
	result := &RunResult{}

	if err := validateRequest(req); err != nil {
		return nil, err
	}

	runner := e.Runner
	if runner == nil {
		runner = execCommand
	}
	env := buildEnv(req.Env)
	secrets := secretValues(req.Env)
	binary := req.Engine.Binary()

	var combined strings.Builder
	// step runs one command, folding its output into the result and stopping
	// the sequence on the first hard failure.
	step := func(name string, args ...string) (bool, error) {
		out, code, err := runner(ctx, req.WorkDir, env, name, args...)
		appendOutput(&combined, redact(out, secrets))
		result.ExitCode = code
		if err != nil && !isChangesPresent(code) {
			return false, err
		}
		if isChangesPresent(code) {
			result.ChangesPresent = true
		}
		return true, nil
	}

	for _, cmd := range e.plannedCommands(req, binary) {
		ok, err := step(cmd.name, cmd.args...)
		result.Output = truncate(combined.String())
		result.Duration = time.Since(start)
		if err != nil {
			result.Errors, result.Warnings = diagnostics(result.Output)
			return result, fmt.Errorf("%s %s failed: %w", cmd.name, strings.Join(cmd.args, " "), err)
		}
		if !ok {
			break
		}
	}

	result.Output = truncate(combined.String())
	result.Counts = ParseResourceCounts(result.Output)
	result.Errors, result.Warnings = diagnostics(result.Output)
	result.Duration = time.Since(start)
	return result, nil
}

// command is one step of a run.
type command struct {
	name string
	args []string
}

// plannedCommands returns the ordered command sequence for a request. Splitting
// it out keeps Run short and makes the argument construction directly testable.
func (e *CommandExecutor) plannedCommands(req RunRequest, binary string) []command {
	shell := e.Shell
	if shell == "" {
		shell = "/bin/sh"
	}

	cmds := make([]command, 0, 6)
	if !req.SkipInit {
		cmds = append(cmds, command{binary, []string{"init", "-input=false", "-no-color"}})
	}
	if req.Workspace != "" && req.Engine != EngineTerragrunt {
		// -or-create keeps a first run from failing on a fresh backend.
		cmds = append(cmds, command{binary, []string{"workspace", "select", "-or-create=true", req.Workspace}})
	}
	for _, hook := range req.Hooks.Pre {
		cmds = append(cmds, command{shell, []string{"-c", hook}})
	}
	cmds = append(cmds, command{binary, OperationArgs(req)})
	for _, hook := range req.Hooks.Post {
		cmds = append(cmds, command{shell, []string{"-c", hook}})
	}
	return cmds
}

// OperationArgs builds the argument list for the main engine operation. It is
// exported because argument construction is the part most worth asserting on.
func OperationArgs(req RunRequest) []string {
	parallelism := req.Parallelism
	if parallelism <= 0 {
		parallelism = 10
	}
	common := []string{
		"-input=false",
		"-no-color",
		fmt.Sprintf("-parallelism=%d", parallelism),
		fmt.Sprintf("-lock=%t", req.Lock),
	}

	var args []string
	switch req.Action {
	case ActionPlan:
		args = append([]string{"plan", "-detailed-exitcode"}, common...)
		args = append(args, fmt.Sprintf("-refresh=%t", req.RefreshBeforePlan))
		if req.PlanFile != "" {
			args = append(args, "-out="+req.PlanFile)
		}
		args = append(args, varArgs(req.Variables)...)
	case ActionRefresh:
		// "terraform refresh" is deprecated; a refresh-only plan is the
		// supported way to compare real state against code, and its detailed
		// exit code is exactly the drift signal we want.
		args = append([]string{"plan", "-refresh-only", "-detailed-exitcode"}, common...)
		args = append(args, varArgs(req.Variables)...)
	case ActionApply:
		args = append([]string{"apply", "-auto-approve"}, common...)
		if req.PlanFile != "" {
			// A saved plan already embeds its variables; passing -var on top
			// of it is an error, so the plan file is the only argument.
			return append(args, req.PlanFile)
		}
		args = append(args, varArgs(req.Variables)...)
	case ActionDestroy:
		args = append([]string{"destroy", "-auto-approve"}, common...)
		args = append(args, varArgs(req.Variables)...)
	default:
		args = append([]string{"plan"}, common...)
	}
	return args
}

// varArgs renders the variable map as -var arguments in a stable order so two
// identical requests produce identical command lines.
func varArgs(vars map[string]string) []string {
	if len(vars) == 0 {
		return nil
	}
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

// validateRequest rejects requests the executor cannot honour, before any
// process is spawned.
func validateRequest(req RunRequest) error {
	if req.WorkDir == "" {
		return errors.New("run request has no working directory")
	}
	if req.Action == "" {
		return errors.New("run request has no action")
	}
	return nil
}

// buildEnv renders the environment map in a stable order. The engine
// deliberately gets only what the caller passed: inheriting the manager's own
// environment would leak its ServiceAccount token into terraform.
func buildEnv(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// secretValues collects the values that must never reach a log or a status.
func secretValues(env map[string]string) []string {
	values := make([]string, 0, len(env))
	for _, v := range env {
		if len(v) >= 4 {
			values = append(values, v)
		}
	}
	// Longest first, so a value that contains another is masked as a whole.
	slices.SortFunc(values, func(a, b string) int { return len(b) - len(a) })
	return values
}

// redact removes every known secret value from the captured output.
func redact(out string, secrets []string) string {
	for _, s := range secrets {
		out = strings.ReplaceAll(out, s, redactedPlaceholder)
	}
	return out
}

// appendOutput folds one command's output into the accumulator, stopping once
// the cap is reached so a runaway module cannot exhaust the manager's memory.
func appendOutput(buf *strings.Builder, out string) {
	if buf.Len() >= maxOutputBytes {
		return
	}
	buf.WriteString(out)
	if !strings.HasSuffix(out, "\n") {
		buf.WriteString("\n")
	}
}

// truncate caps the output, keeping the tail: engine failures are reported at
// the end of the stream.
func truncate(out string) string {
	if len(out) <= maxOutputBytes {
		return out
	}
	return "...[truncated]...\n" + out[len(out)-maxOutputBytes:]
}

// isChangesPresent reports whether the exit code is the "plan is non-empty"
// signal rather than a real failure.
func isChangesPresent(code int) bool {
	return code == exitCodeChangesPresent
}

// planCountRe matches "Plan: 1 to add, 2 to change, 3 to destroy."
var planCountRe = regexp.MustCompile(`Plan:\s+(\d+)\s+to add,\s+(\d+)\s+to change,\s+(\d+)\s+to destroy`)

// applyCountRe matches "Apply complete! Resources: 1 added, 2 changed, 3 destroyed."
var applyCountRe = regexp.MustCompile(`Resources:\s+(\d+)\s+added,\s+(\d+)\s+changed,\s+(\d+)\s+destroyed`)

// stateCountRe matches the resource total reported by a refresh-only plan.
var stateCountRe = regexp.MustCompile(`(\d+)\s+resources? (?:are|is) up-to-date`)

// ParseResourceCounts extracts the resource delta from engine output. Apply
// output wins over plan output because a request may contain both.
func ParseResourceCounts(out string) ResourceCounts {
	var counts ResourceCounts
	if m := planCountRe.FindStringSubmatch(out); m != nil {
		counts.Created, counts.Updated, counts.Deleted = atoi32(m[1]), atoi32(m[2]), atoi32(m[3])
	}
	if m := applyCountRe.FindStringSubmatch(out); m != nil {
		counts.Created, counts.Updated, counts.Deleted = atoi32(m[1]), atoi32(m[2]), atoi32(m[3])
	}
	if m := stateCountRe.FindStringSubmatch(out); m != nil {
		counts.Unchanged = atoi32(m[1])
	}
	return counts
}

// diagnostics scrapes the engine's Error and Warning blocks. Only the headline
// is kept: the indented detail is already in the truncated output.
func diagnostics(out string) (errs []string, warns []string) {
	for line := range strings.SplitSeq(out, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Error: "):
			errs = appendCapped(errs, strings.TrimPrefix(trimmed, "Error: "))
		case strings.HasPrefix(trimmed, "Warning: "):
			warns = appendCapped(warns, strings.TrimPrefix(trimmed, "Warning: "))
		}
	}
	return errs, warns
}

// maxDiagnostics bounds each diagnostic list so the status stays small.
const maxDiagnostics = 20

// appendCapped appends while keeping the list bounded and free of duplicates.
func appendCapped(list []string, msg string) []string {
	if msg == "" || len(list) >= maxDiagnostics || slices.Contains(list, msg) {
		return list
	}
	return append(list, msg)
}

func atoi32(s string) int32 {
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		return 0
	}
	return int32(n)
}

// execCommand is the default CommandRunner. It merges stdout and stderr because
// the engine interleaves diagnostics across both and the ordering matters when
// a human reads the status.
func execCommand(ctx context.Context, dir string, env []string, name string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	// WaitDelay bounds how long a child that ignores the kill signal may hold
	// the reconcile worker after the context is cancelled.
	cmd.WaitDelay = 10 * time.Second

	err := cmd.Run()
	code := cmd.ProcessState.ExitCode()

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && isChangesPresent(code) {
		// -detailed-exitcode reports "changes present" as a non-zero status.
		return buf.String(), code, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return buf.String(), code, fmt.Errorf("engine run aborted: %w", ctxErr)
	}
	return buf.String(), code, err
}
