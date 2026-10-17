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

// Package runner executes one Flare inside its Kubernetes Job: it fetches the
// repository, runs the engine (Terraform or OpenTofu, optionally through
// Terragrunt), computes the drift from the JSON plan, enforces the destroy
// protection, and reports back through its log.
//
// This is the only place in stellarCD that runs IaC binaries. The controller
// never imports it: it builds Jobs that run cmd/runner.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// CommandRunner executes one command and returns its combined, redacted output
// and exit code. It is the seam tests replace.
type CommandRunner func(ctx context.Context, dir string, env []string, name string, args ...string) (string, int, error)

// redactedPlaceholder replaces every secret value found in the output.
const redactedPlaceholder = "***REDACTED***"

// maxCapturedBytes bounds what is kept in memory per command. The log gets the
// whole stream; the status only ever needs the tail.
const maxCapturedBytes = 64 << 10

// Redactor masks known secret values. Values shorter than four characters are
// ignored: masking every "1" or "true" would destroy the output for nothing.
type Redactor struct {
	values []string
}

// NewRedactor builds a redactor over the given secret values.
func NewRedactor(values []string) *Redactor {
	kept := make([]string, 0, len(values))
	for _, v := range values {
		if len(v) >= 4 {
			kept = append(kept, v)
		}
	}
	// Longest first, so a value that contains another is masked as a whole.
	slices.SortFunc(kept, func(a, b string) int { return len(b) - len(a) })
	return &Redactor{values: kept}
}

// Redact masks every known secret value in s.
func (r *Redactor) Redact(s string) string {
	if r == nil {
		return s
	}
	for _, v := range r.values {
		s = strings.ReplaceAll(s, v, redactedPlaceholder)
	}
	return s
}

// Shell runs commands for the runner, streaming their output, redacted, to the
// log while keeping a bounded copy for the result.
type Shell struct {
	// Log receives the redacted output as it is produced. It is the pod log.
	Log io.Writer
	// Redactor masks secret values before anything is written or kept.
	Redactor *Redactor
	// WaitDelay bounds how long a child that ignores the kill signal may
	// outlive its context.
	WaitDelay time.Duration
}

// Run implements CommandRunner.
func (s *Shell) Run(ctx context.Context, dir string, env []string, name string, args ...string) (string, int, error) {
	var captured cappedBuffer
	sink := &redactingWriter{redactor: s.Redactor, out: io.MultiWriter(s.logWriter(), &captured)}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = sink
	cmd.Stderr = sink
	cmd.WaitDelay = s.waitDelay()

	err := cmd.Run()
	sink.Flush()
	return captured.String(), exitCode(cmd), wrapRunError(ctx, name, err)
}

// Capture runs a command whose stdout is data rather than log: a JSON plan
// carries attribute values, so it goes to the caller and never to the log.
// Stderr is still streamed, redacted.
func (s *Shell) Capture(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	var stdout bytes.Buffer
	sink := &redactingWriter{redactor: s.Redactor, out: s.logWriter()}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = &stdout
	cmd.Stderr = sink
	cmd.WaitDelay = s.waitDelay()

	err := cmd.Run()
	sink.Flush()
	return stdout.Bytes(), wrapRunError(ctx, name, err)
}

func (s *Shell) logWriter() io.Writer {
	if s.Log == nil {
		return io.Discard
	}
	return s.Log
}

func (s *Shell) waitDelay() time.Duration {
	if s.WaitDelay > 0 {
		return s.WaitDelay
	}
	return 10 * time.Second
}

func exitCode(cmd *exec.Cmd) int {
	if cmd.ProcessState == nil {
		return -1
	}
	return cmd.ProcessState.ExitCode()
}

func wrapRunError(ctx context.Context, name string, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%s aborted: %w", name, ctxErr)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("%s exited with status %d", name, exitErr.ExitCode())
	}
	return fmt.Errorf("failed to run %s: %w", name, err)
}

// redactingWriter masks secrets line by line. Working on whole lines matters:
// a secret split across two Write calls would slip through a per-call filter.
type redactingWriter struct {
	mu       sync.Mutex
	redactor *Redactor
	out      io.Writer
	pending  []byte
}

func (w *redactingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, p...)
	for {
		idx := bytes.IndexByte(w.pending, '\n')
		if idx < 0 {
			break
		}
		line := w.pending[:idx+1]
		if _, err := io.WriteString(w.out, w.redactor.Redact(string(line))); err != nil {
			return 0, err
		}
		w.pending = w.pending[idx+1:]
	}
	return len(p), nil
}

// Flush writes a trailing partial line, if any.
func (w *redactingWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) == 0 {
		return
	}
	_, _ = io.WriteString(w.out, w.redactor.Redact(string(w.pending))+"\n")
	w.pending = nil
}

// cappedBuffer keeps the tail of a stream: engine failures are reported last.
type cappedBuffer struct {
	buf []byte
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.buf = append(c.buf, p...)
	if len(c.buf) > maxCapturedBytes {
		c.buf = c.buf[len(c.buf)-maxCapturedBytes:]
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return string(c.buf) }

// tail keeps the last limit bytes of a blob, marking the cut.
func tail(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return "...[truncated]...\n" + text[len(text)-limit:]
}

// maxDiagnostics bounds each diagnostic list so the status stays small.
const maxDiagnostics = 20

// diagnostics scrapes the engine's Error and Warning headlines.
func diagnostics(out string) (errs []string, warns []string) {
	for line := range strings.SplitSeq(out, "\n") {
		trimmed := strings.TrimSpace(line)
		// Terragrunt prefixes unit output, e.g. "[apps/x] terraform: Error: ...".
		if i := strings.Index(trimmed, "Error: "); i >= 0 {
			errs = appendCapped(errs, strings.TrimSpace(trimmed[i+len("Error: "):]))
			continue
		}
		if i := strings.Index(trimmed, "Warning: "); i >= 0 {
			warns = appendCapped(warns, strings.TrimSpace(trimmed[i+len("Warning: "):]))
		}
	}
	return errs, warns
}

func appendCapped(list []string, msg string) []string {
	if msg == "" || len(list) >= maxDiagnostics || slices.Contains(list, msg) {
		return list
	}
	return append(list, msg)
}

// ChildEnv builds the environment for the engine and its plugins from the
// runner's own: the controller's request and nonce are removed, and the
// automation switches every engine and Terragrunt need are added.
func ChildEnv(base []string) []string {
	env := make([]string, 0, len(base)+6)
	for _, kv := range base {
		if strings.HasPrefix(kv, "STELLARCD_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"TF_IN_AUTOMATION=1",
		"TF_INPUT=0",
		"CHECKPOINT_DISABLE=1",
		"TG_NON_INTERACTIVE=true",
		"TG_NO_COLOR=true",
	)
}
