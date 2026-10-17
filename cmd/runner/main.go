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

// Command runner executes one Flare inside its Kubernetes Job. The controller
// describes the run in the STELLARCD_RUN environment variable and reads the
// outcome back from this process's log.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/nrx-ops/stellarCD/internal/runner"
	"github.com/nrx-ops/stellarCD/internal/runspec"
	"github.com/nrx-ops/stellarCD/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Printf("%+v\n", version.Get())
		return
	}
	os.Exit(run())
}

func run() int {
	req, err := runspec.DecodeRequest(os.Getenv(runspec.EnvRequest))
	if err != nil {
		fmt.Fprintln(os.Stderr, "stellarcd runner:", err)
		return 2
	}
	nonce := os.Getenv(runspec.EnvNonce)
	if nonce == "" {
		fmt.Fprintln(os.Stderr, "stellarcd runner:", runspec.EnvNonce, "is empty")
		return 2
	}

	// SIGTERM arrives when the Job's deadline fires or the Flare is cancelled.
	// Cancelling the context lets the engine stop between resources instead of
	// being killed mid-write.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	shell := &runner.Shell{Log: os.Stdout, Redactor: runner.NewRedactor(secretValues(req))}
	r := &runner.Runner{
		Request: req,
		Nonce:   nonce,
		Log:     os.Stdout,
		Exec:    shell.Run,
		Capture: shell.Capture,
		Env:     runner.ChildEnv(os.Environ()),
	}
	if result := r.Run(ctx); !result.Succeeded {
		// The result already carries the failure; the exit code only makes
		// the Job itself read as failed in kubectl.
		return 1
	}
	return 0
}

// secretValues collects the values of the env vars the controller marked as
// secret, plus the mounted git credentials, so none of them reaches the log.
func secretValues(req *runspec.Request) []string {
	values := make([]string, 0, len(req.SecretEnv)+2)
	for _, name := range req.SecretEnv {
		if v := os.Getenv(name); v != "" {
			values = append(values, v)
		}
	}
	for _, key := range []string{"password", "token", "sshPrivateKey"} {
		if data, err := os.ReadFile(runspec.GitCredentialsDir + "/" + key); err == nil {
			values = append(values, string(data))
		}
	}
	return values
}
