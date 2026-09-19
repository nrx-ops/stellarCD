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

package shared

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrepareClonesIntoAnEmptyDirectory is a regression test. Credential files
// used to be staged inside the working copy, so the very first "git clone <url> ."
// hit "destination path '.' already exists and is not an empty directory" and no
// authenticated checkout could ever succeed.
func TestPrepareClonesIntoAnEmptyDirectory(t *testing.T) {
	var sawClone bool

	w := &GitWorkspace{Root: t.TempDir()}
	w.Runner = func(
		_ context.Context, dir string, _ []string, name string, args ...string,
	) (string, int, error) {
		if name == "git" && len(args) > 0 && args[0] == "clone" {
			sawClone = true
			entries, err := os.ReadDir(dir)
			if err != nil {
				return "", 1, err
			}
			if len(entries) != 0 {
				names := make([]string, 0, len(entries))
				for _, e := range entries {
					names = append(names, e.Name())
				}
				return "", 1, fmt.Errorf(
					"git would refuse to clone into a non-empty directory holding %v", names)
			}
			// Stand in for the clone: git leaves a .git directory behind.
			return "", 0, os.MkdirAll(filepath.Join(dir, ".git"), 0o700)
		}
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return strings.Repeat("a", 40) + "\n", 0, nil
		}
		return "", 0, nil
	}

	checkout, err := w.Prepare(context.Background(), CheckoutRequest{
		Key:         "universe/galaxy/astral",
		URL:         "https://github.com/4sh/cinng-iac",
		Branch:      "main",
		Path:        ".",
		Credentials: map[string][]byte{SecretKeyToken: []byte("ghp_example")},
	})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if !sawClone {
		t.Fatal("the clone branch never ran, so the regression is untested")
	}

	// The credential file must exist, and must not be inside the working copy.
	if len(checkout.cleanupPaths) != 1 {
		t.Fatalf("got cleanup paths %v, want exactly one credential file", checkout.cleanupPaths)
	}
	credPath := checkout.cleanupPaths[0]
	if _, statErr := os.Stat(credPath); statErr != nil {
		t.Fatalf("credential file is missing: %v", statErr)
	}
	rel, relErr := filepath.Rel(checkout.RepoDir, credPath)
	if relErr == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("credential file %q sits inside the working copy %q: "+
			"git clean -fdx would delete it mid-run", credPath, checkout.RepoDir)
	}

	if err := w.Cleanup(checkout); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if _, statErr := os.Stat(credPath); !os.IsNotExist(statErr) {
		t.Fatal("credential file survived cleanup")
	}
	if checkout.credDir != "" {
		t.Fatal("credential staging directory was not released")
	}
}

// TestPrepareWithoutCredentialsStagesNothing keeps the public-repository path
// free of a staging directory it would never use.
func TestPrepareWithoutCredentialsStagesNothing(t *testing.T) {
	w := &GitWorkspace{Root: t.TempDir()}
	w.Runner = func(
		_ context.Context, dir string, _ []string, name string, args ...string,
	) (string, int, error) {
		if name == "git" && len(args) > 0 && args[0] == "clone" {
			return "", 0, os.MkdirAll(filepath.Join(dir, ".git"), 0o700)
		}
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return strings.Repeat("b", 40) + "\n", 0, nil
		}
		return "", 0, nil
	}

	checkout, err := w.Prepare(context.Background(), CheckoutRequest{
		Key:    "universe/galaxy/public",
		URL:    "https://github.com/4sh/public",
		Branch: "main",
		Path:   ".",
	})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if checkout.credDir != "" {
		t.Fatalf("got staging directory %q, want none without credentials", checkout.credDir)
	}
	if _, statErr := os.Stat(filepath.Join(w.Root, ".credentials")); !os.IsNotExist(statErr) {
		t.Fatal("the credential staging root was created for a checkout with no credentials")
	}
}
