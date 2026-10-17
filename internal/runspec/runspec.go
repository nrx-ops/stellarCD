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

// Package runspec is the contract between the controller and the runner that
// executes a Flare inside its Job: what the controller asks for, and how the
// runner reports back.
//
// It is deliberately free of any process execution, so the controller can
// depend on it without pulling the runner's os/exec code into the manager.
//
// The runner holds no cluster credentials, so it cannot write the Flare status
// itself. It reports through its own log instead: step markers while it works
// and a result block at the end, both tagged with a per-Job nonce. The
// controller reads the pod log, which is why it needs get on pods/log and
// nothing in the Job needs a ServiceAccount token.
package runspec

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
)

// Environment variables the controller sets on the runner container. The
// runner strips both from the environment it hands the engine.
//
// The nonce stops a marker line that is not the runner's from being taken as
// its result: an engine message or a hook that happens to replay an old log.
// It is not a defence against hostile repository code, which runs under the
// same user and could read it from /proc; such code already holds the cloud
// credentials, so forging a Flare status would gain it nothing.
const (
	EnvRequest = "STELLARCD_RUN"
	EnvNonce   = "STELLARCD_NONCE"
)

// Fixed paths inside the runner pod.
const (
	// WorkDir is the writable emptyDir holding the checkout and the plans.
	WorkDir = "/work"
	// GitCredentialsDir is where the git credentials Secret is mounted.
	GitCredentialsDir = "/var/run/stellarcd/git"
	// BackendConfigDir is where the Galaxy's rendered backend ConfigMap is mounted.
	BackendConfigDir = "/var/run/stellarcd/backend"
	// BackendConfigFile is the rendered partial backend configuration.
	BackendConfigFile = BackendConfigDir + "/backend.hcl"
)

// MaxResourceChanges caps the per-resource list carried back to the status.
// The full plan is in the pod log; the status only needs enough to show what
// is going on.
const MaxResourceChanges = 200

// MaxPendingDestroy caps the destroy allowlist. Unlike the change list it is
// never truncated: a confirmation must cover exactly what the user saw, so a
// plan destroying more than this is refused instead.
const MaxPendingDestroy = 1000

// Request is everything the runner needs that is not a secret. Secrets reach
// the pod through Kubernetes alone: env vars for cloud credentials, a mounted
// file for git. The request only names the env vars to redact.
type Request struct {
	Action            corev1alpha1.FlareAction `json:"action"`
	Engine            corev1alpha1.Engine      `json:"engine"`
	EngineVersion     string                   `json:"engineVersion,omitempty"`
	Terragrunt        *Terragrunt              `json:"terragrunt,omitempty"`
	Scope             corev1alpha1.FlareScope  `json:"scope"`
	UnitPath          string                   `json:"unitPath,omitempty"`
	Repository        Repository               `json:"repository"`
	Workspace         string                   `json:"workspace,omitempty"`
	Parallelism       int32                    `json:"parallelism"`
	Lock              bool                     `json:"lock"`
	RefreshBeforePlan bool                     `json:"refreshBeforePlan"`
	// Variables are non-sensitive -var inputs. Anything secret arrives as an
	// env var through the Job spec instead: arguments are visible in the
	// process table.
	Variables map[string]string `json:"variables,omitempty"`
	Hooks     Hooks             `json:"hooks,omitempty"`
	// AllowedDestroy is the set of resources the user confirmed may be deleted
	// or replaced. An apply whose plan destroys anything outside it is held.
	AllowedDestroy []string `json:"allowedDestroy,omitempty"`
	// SecretEnv names the env vars whose values must never reach the log.
	SecretEnv []string `json:"secretEnv,omitempty"`
	// BackendConfig reports that BackendConfigFile is mounted.
	BackendConfig bool `json:"backendConfig,omitempty"`
}

// Terragrunt mirrors the Astral's terragrunt block.
type Terragrunt struct {
	Version       string                      `json:"version,omitempty"`
	Mode          corev1alpha1.TerragruntMode `json:"mode"`
	ExcludedUnits []string                    `json:"excludedUnits,omitempty"`
}

// Repository locates the code to run.
type Repository struct {
	URL      string `json:"url"`
	Branch   string `json:"branch,omitempty"`
	Revision string `json:"revision,omitempty"`
	Path     string `json:"path,omitempty"`
}

// Hooks are the shell commands run around the engine invocation.
type Hooks struct {
	PrePlan   []string `json:"prePlan,omitempty"`
	PostPlan  []string `json:"postPlan,omitempty"`
	PreApply  []string `json:"preApply,omitempty"`
	PostApply []string `json:"postApply,omitempty"`
}

// RunAll reports whether the request runs every unit below the path.
func (r *Request) RunAll() bool {
	return r.Terragrunt != nil && r.Terragrunt.Mode == corev1alpha1.TerragruntModeRunAll &&
		r.Scope != corev1alpha1.FlareScopeUnit
}

// Mutates reports whether the request may change real infrastructure.
func (r *Request) Mutates() bool {
	return r.Action.Mutates()
}

// Encode renders the request for the EnvRequest variable.
func (r *Request) Encode() (string, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("failed to encode the run request: %w", err)
	}
	return string(data), nil
}

// DecodeRequest parses the EnvRequest variable.
func DecodeRequest(raw string) (*Request, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("%s is empty", EnvRequest)
	}
	var req Request
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return nil, fmt.Errorf("failed to decode %s: %w", EnvRequest, err)
	}
	return &req, nil
}

// Result is what the runner reports when it is done.
type Result struct {
	// Succeeded is false when a step failed. A plan held for a destroy
	// confirmation is a success with NeedsConfirmation set.
	Succeeded bool `json:"succeeded"`
	// Step is the last step the runner reached.
	Step corev1alpha1.FlareStep `json:"step"`
	// Message is a one-line summary for the status.
	Message string `json:"message,omitempty"`
	// Revision is the commit SHA that was run.
	Revision string `json:"revision,omitempty"`
	// Applied reports that an apply actually ran to completion.
	Applied bool `json:"applied,omitempty"`
	// NeedsConfirmation reports that the plan destroys resources outside
	// AllowedDestroy, so the apply was not attempted.
	NeedsConfirmation bool                           `json:"needsConfirmation,omitempty"`
	PlanSummary       *corev1alpha1.PlanSummary      `json:"planSummary,omitempty"`
	Changes           []corev1alpha1.ResourceChange  `json:"changes,omitempty"`
	ChangesTruncated  bool                           `json:"changesTruncated,omitempty"`
	PendingDestroy    []string                       `json:"pendingDestroy,omitempty"`
	Units             []corev1alpha1.FlareUnitResult `json:"units,omitempty"`
	Errors            []string                       `json:"errors,omitempty"`
	Warnings          []string                       `json:"warnings,omitempty"`
	// Output is the redacted tail of the engine output.
	Output string `json:"output,omitempty"`
}

// marker prefixes every line the runner writes for the controller.
const marker = "::stellarcd::"

// chunkSize keeps every result line well under the container runtime's line
// limit, past which a log line is split into partial lines.
const chunkSize = 4096

// StepLine renders a step marker.
func StepLine(nonce string, step corev1alpha1.FlareStep) string {
	return marker + nonce + "::step::" + string(step)
}

// ResultLines renders a result as one or more marker lines.
func ResultLines(nonce string, result *Result) ([]string, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("failed to encode the run result: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(data)

	total := (len(encoded) + chunkSize - 1) / chunkSize
	if total == 0 {
		total = 1
	}
	lines := make([]string, 0, total)
	for i := 0; i < total; i++ {
		end := min((i+1)*chunkSize, len(encoded))
		lines = append(lines, fmt.Sprintf("%s%s::result::%d/%d::%s",
			marker, nonce, i+1, total, encoded[i*chunkSize:end]))
	}
	return lines, nil
}

// ErrNoResult means the log holds no complete result for the nonce.
var ErrNoResult = errors.New("the runner log holds no result")

// ParseLog scans a runner log for the nonce's markers. It returns the last
// step reached and the last complete result. Lines without the nonce are
// ignored, so stray output cannot pass for a result.
func ParseLog(log, nonce string) (corev1alpha1.FlareStep, *Result, error) {
	prefix := marker + nonce + "::"
	var step corev1alpha1.FlareStep
	var chunks []string
	var complete string

	scanner := bufio.NewScanner(strings.NewReader(log))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		idx := strings.Index(line, prefix)
		if idx < 0 {
			continue
		}
		body := line[idx+len(prefix):]

		if rest, ok := strings.CutPrefix(body, "step::"); ok {
			step = corev1alpha1.FlareStep(rest)
			continue
		}
		rest, ok := strings.CutPrefix(body, "result::")
		if !ok {
			continue
		}
		position, payload, ok := strings.Cut(rest, "::")
		if !ok {
			continue
		}
		index, total, ok := parsePosition(position)
		if !ok {
			continue
		}
		if index == 1 {
			chunks = chunks[:0]
		}
		if len(chunks) != index-1 {
			// Out of order: a truncated or interleaved block. Drop it.
			chunks = chunks[:0]
			continue
		}
		chunks = append(chunks, payload)
		if index == total {
			complete = strings.Join(chunks, "")
			chunks = chunks[:0]
		}
	}
	if err := scanner.Err(); err != nil {
		return step, nil, fmt.Errorf("failed to scan the runner log: %w", err)
	}
	if complete == "" {
		return step, nil, ErrNoResult
	}

	data, err := base64.StdEncoding.DecodeString(complete)
	if err != nil {
		return step, nil, fmt.Errorf("failed to decode the runner result: %w", err)
	}
	var result Result
	if err := json.Unmarshal(data, &result); err != nil {
		return step, nil, fmt.Errorf("failed to parse the runner result: %w", err)
	}
	return step, &result, nil
}

func parsePosition(position string) (int, int, bool) {
	left, right, ok := strings.Cut(position, "/")
	if !ok {
		return 0, 0, false
	}
	index, err1 := strconv.Atoi(left)
	total, err2 := strconv.Atoi(right)
	if err1 != nil || err2 != nil || index < 1 || total < 1 || index > total {
		return 0, 0, false
	}
	return index, total, true
}
