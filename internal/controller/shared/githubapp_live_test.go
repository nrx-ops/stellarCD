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
	"os"
	"testing"
)

// The stubbed tests cover the request shapes; only a real App can confirm that
// GitHub accepts the assertion and that git authenticates with the result.
// This test therefore talks to the live API and is skipped unless all three
// variables are set, so it never runs in CI:
//
//	STELLARCD_LIVE_APP_ID    numeric App ID, or the App client ID
//	STELLARCD_LIVE_APP_KEY   path to the PEM private key
//	STELLARCD_LIVE_REPO      HTTPS clone URL the App is installed on
//	STELLARCD_LIVE_BRANCH    branch to check out, defaults to main
//	STELLARCD_LIVE_PATH      module directory inside the repository, defaults to "."
//
// The minted token is never logged: a live installation token is a credential
// for as long as it lives.
func liveGitHubAppCredentials(t *testing.T) (map[string][]byte, string, string, string) {
	t.Helper()
	appID := os.Getenv("STELLARCD_LIVE_APP_ID")
	keyPath := os.Getenv("STELLARCD_LIVE_APP_KEY")
	repo := os.Getenv("STELLARCD_LIVE_REPO")
	if appID == "" || keyPath == "" || repo == "" {
		t.Skip("live GitHub App credentials not provided")
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("failed to read %s: %v", keyPath, err)
	}
	branch := os.Getenv("STELLARCD_LIVE_BRANCH")
	if branch == "" {
		branch = "main"
	}
	modulePath := os.Getenv("STELLARCD_LIVE_PATH")
	if modulePath == "" {
		modulePath = "."
	}
	return map[string][]byte{
		SecretKeyGitHubAppID:         []byte(appID),
		SecretKeyGitHubAppPrivateKey: key,
	}, repo, branch, modulePath
}

// TestLiveGitHubAppInstallationToken checks that GitHub accepts the assertion,
// resolves the installation and mints a token.
func TestLiveGitHubAppInstallationToken(t *testing.T) {
	creds, repo, _, _ := liveGitHubAppCredentials(t)

	auth := &githubAppAuthenticator{}
	token, err := auth.installationToken(context.Background(), repo, creds)
	if err != nil {
		t.Fatalf("installationToken failed: %v", err)
	}
	if token == "" {
		t.Fatal("got an empty token")
	}
	t.Logf("minted an installation token for %s: %d characters", repo, len(token))
}

// TestLiveGitHubAppClone drives the whole provider: mint, stage, and let git
// authenticate a real private clone with the result.
func TestLiveGitHubAppClone(t *testing.T) {
	creds, repo, branch, modulePath := liveGitHubAppCredentials(t)

	w := NewGitWorkspace(t.TempDir())
	checkout, err := w.Prepare(context.Background(), CheckoutRequest{
		Key:         "live/githubapp",
		URL:         repo,
		Branch:      branch,
		Path:        modulePath,
		Credentials: creds,
	})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	defer func() { _ = w.Cleanup(checkout) }()

	if len(checkout.Revision) != 40 {
		t.Fatalf("got revision %q, want a 40-character SHA", checkout.Revision)
	}
	if _, statErr := os.Stat(checkout.ModuleDir); statErr != nil {
		t.Fatalf("module directory is missing: %v", statErr)
	}
	t.Logf("cloned %s at %s, module directory %s", repo, checkout.Revision, checkout.ModuleDir)
}
