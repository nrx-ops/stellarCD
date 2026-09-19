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
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Well-known keys read from a VCS credentials Secret. A Secret carries exactly
// one of these layouts; see materialiseCredentials for the precedence between
// them. The GitHub App keys live in githubapp.go.
const (
	// SecretKeySSHPrivateKey holds a PEM private key for SSH clone URLs.
	SecretKeySSHPrivateKey = "sshPrivateKey"
	// SecretKeyUsername and SecretKeyPassword hold HTTP basic credentials.
	SecretKeyUsername = "username"
	SecretKeyPassword = "password"
	// SecretKeyToken is accepted as an alias for the password, which is what
	// GitHub, GitLab and Gitea personal access tokens actually are.
	SecretKeyToken = "token"
)

// CheckoutRequest describes the working copy a Flare needs.
type CheckoutRequest struct {
	// Key uniquely identifies the checkout on disk, e.g. "<universe>/<galaxy>/<astral>".
	Key string
	// URL is the clone URL.
	URL string
	// Branch is the branch to check out.
	Branch string
	// Path is the module directory inside the repository.
	Path string
	// Credentials are the raw Secret entries. They are written to files with
	// 0600 permissions and referenced through environment variables, never
	// interpolated into the clone URL: command lines are world-readable in
	// /proc, environments are not.
	Credentials map[string][]byte
}

// Checkout is a prepared working copy.
type Checkout struct {
	// RepoDir is the repository root.
	RepoDir string
	// ModuleDir is the directory the engine runs in.
	ModuleDir string
	// Env holds the git and engine environment additions, e.g. GIT_SSH_COMMAND.
	Env map[string]string
	// Revision is the resolved commit SHA.
	Revision string

	// credDir holds the staged credential files. It sits outside the working
	// copy on purpose: git refuses to clone into a non-empty directory, and
	// "git clean -fdx" would delete anything staged inside one.
	credDir string
	// cleanupPaths are the credential files to shred once the run is over.
	cleanupPaths []string
}

// WorkspaceProvider materialises the working copy for a run.
type WorkspaceProvider interface {
	Prepare(ctx context.Context, req CheckoutRequest) (*Checkout, error)
	Cleanup(checkout *Checkout) error
}

// GitWorkspace clones repositories with the git CLI. Shelling out keeps the
// operator free of a Git library and lets operators drop in their own git
// wrapper, at the cost of requiring the binary in the runner image.
type GitWorkspace struct {
	// Root is the directory holding every checkout.
	Root string
	// Runner defaults to execCommand when nil.
	Runner CommandRunner
	// HTTPClient talks to the GitHub API when a Secret carries GitHub App
	// credentials. It defaults to defaultGitHubHTTPClient when nil.
	HTTPClient *http.Client
}

// NewGitWorkspace returns a provider rooted at root.
func NewGitWorkspace(root string) *GitWorkspace {
	return &GitWorkspace{Root: root, Runner: execCommand}
}

// Prepare clones or updates the repository and returns the module directory.
func (w *GitWorkspace) Prepare(ctx context.Context, req CheckoutRequest) (*Checkout, error) {
	if req.URL == "" {
		return nil, fmt.Errorf("checkout %q has no repository URL", req.Key)
	}
	repoDir := filepath.Join(w.Root, filepath.FromSlash(req.Key))
	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create workspace %q: %w", repoDir, err)
	}

	checkout := &Checkout{RepoDir: repoDir, Env: map[string]string{}}
	if len(req.Credentials) > 0 {
		credDir, err := w.stagingDir()
		if err != nil {
			return nil, err
		}
		checkout.credDir = credDir
	}
	if err := w.materialiseCredentials(ctx, checkout.credDir, req.URL, req.Credentials, checkout); err != nil {
		_ = w.Cleanup(checkout)
		return nil, err
	}

	branch := req.Branch
	if branch == "" {
		branch = "main"
	}
	if err := w.sync(ctx, repoDir, req.URL, branch, checkout); err != nil {
		_ = w.Cleanup(checkout)
		return nil, err
	}

	revision, err := w.revision(ctx, repoDir, checkout)
	if err != nil {
		_ = w.Cleanup(checkout)
		return nil, err
	}
	checkout.Revision = revision

	moduleDir, err := resolveModuleDir(repoDir, req.Path)
	if err != nil {
		_ = w.Cleanup(checkout)
		return nil, err
	}
	checkout.ModuleDir = moduleDir
	return checkout, nil
}

// sync clones on first use and fetches afterwards, so a busy Astral does not
// re-download its whole history on every Flare.
func (w *GitWorkspace) sync(ctx context.Context, repoDir, url, branch string, checkout *Checkout) error {
	env := gitEnv(checkout.Env)
	runner := w.runner()

	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		out, _, cloneErr := runner(ctx, repoDir, env, "git",
			"clone", "--depth=1", "--branch", branch, "--single-branch", url, ".")
		if cloneErr != nil {
			return fmt.Errorf("failed to clone repository: %w: %s", cloneErr, lastLine(out))
		}
		return nil
	}

	if out, _, err := runner(ctx, repoDir, env, "git",
		"fetch", "--depth=1", "origin", branch); err != nil {
		return fmt.Errorf("failed to fetch %q: %w: %s", branch, err, lastLine(out))
	}
	if out, _, err := runner(ctx, repoDir, env, "git",
		"reset", "--hard", "origin/"+branch); err != nil {
		return fmt.Errorf("failed to reset to origin/%s: %w: %s", branch, err, lastLine(out))
	}
	// A stale file left by a previous run would be picked up by the engine as
	// if it were part of the tracked revision.
	if out, _, err := runner(ctx, repoDir, env, "git", "clean", "-fdx", "--exclude=.terraform"); err != nil {
		return fmt.Errorf("failed to clean workspace: %w: %s", err, lastLine(out))
	}
	return nil
}

// revision resolves the checked-out commit so the Flare can report exactly what
// it ran against.
func (w *GitWorkspace) revision(ctx context.Context, repoDir string, checkout *Checkout) (string, error) {
	out, _, err := w.runner()(ctx, repoDir, gitEnv(checkout.Env), "git", "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("failed to resolve HEAD: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// materialiseCredentials writes the Secret entries to 0600 files and points git
// at them through the environment. Credentials never appear on a command line:
// /proc/<pid>/cmdline is world-readable, a 0600 file is not.
//
// Exactly one layout is used per checkout, in this order: an SSH key, then
// GitHub App credentials, then a static username/password or token. A Secret
// that carries none of them is an error rather than a silent anonymous clone,
// which fails much later with a message that points at the repository instead
// of at the Secret.
func (w *GitWorkspace) materialiseCredentials(
	ctx context.Context,
	credDir, cloneURL string,
	creds map[string][]byte,
	checkout *Checkout,
) error {
	if len(creds) == 0 {
		return nil
	}

	if key, ok := creds[SecretKeySSHPrivateKey]; ok && len(key) > 0 {
		path := filepath.Join(credDir, ".stellarcd-ssh-key")
		if err := os.WriteFile(path, ensureTrailingNewline(key), 0o600); err != nil {
			return fmt.Errorf("failed to stage SSH key: %w", err)
		}
		checkout.cleanupPaths = append(checkout.cleanupPaths, path)
		// accept-new pins the host key on first contact instead of disabling
		// verification outright, which "no" would do.
		checkout.Env["GIT_SSH_COMMAND"] = fmt.Sprintf(
			"ssh -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new", path)
		return nil
	}

	if hasGitHubAppCredentials(creds) {
		// The token is minted here, per checkout, and expires about an hour
		// later. It is staged exactly like a personal access token: from git's
		// point of view an installation token is just a password.
		token, err := w.githubApp().installationToken(ctx, cloneURL, creds)
		if err != nil {
			return err
		}
		return w.stageBasicAuth(credDir, cloneURL, githubAppUsername, token, checkout)
	}

	password := string(creds[SecretKeyPassword])
	if password == "" {
		password = string(creds[SecretKeyToken])
	}
	if password == "" {
		return fmt.Errorf(
			"VCS credentials Secret carries none of %q, %q, %q/%q or %q plus %q",
			SecretKeySSHPrivateKey, SecretKeyToken, SecretKeyUsername, SecretKeyPassword,
			SecretKeyGitHubAppID, SecretKeyGitHubAppPrivateKey)
	}
	username := string(creds[SecretKeyUsername])
	if username == "" {
		// Every supported provider accepts an arbitrary username next to a
		// personal access token.
		username = "stellarcd"
	}
	return w.stageBasicAuth(credDir, cloneURL, username, password, checkout)
}

// stagingDir creates the private directory holding one checkout's credential
// files. It is deliberately not inside the working copy: git refuses to clone
// into a non-empty directory, and the "git clean -fdx" in sync would delete
// anything staged there on the next run.
func (w *GitWorkspace) stagingDir() (string, error) {
	root := filepath.Join(w.Root, ".credentials")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("failed to create the credential staging root: %w", err)
	}
	dir, err := os.MkdirTemp(root, "checkout-")
	if err != nil {
		return "", fmt.Errorf("failed to create the credential staging directory: %w", err)
	}
	return dir, nil
}

// githubApp returns the authenticator used to mint installation tokens.
func (w *GitWorkspace) githubApp() *githubAppAuthenticator {
	return &githubAppAuthenticator{client: w.HTTPClient}
}

// stageBasicAuth writes a git credential-store file and points git at it. It
// backs both the static token path and the GitHub App path, which differ only
// in where the password came from.
func (w *GitWorkspace) stageBasicAuth(
	credDir, cloneURL, username, password string,
	checkout *Checkout,
) error {
	entry, err := credentialStoreEntry(cloneURL, username, password)
	if err != nil {
		return err
	}
	path := filepath.Join(credDir, ".stellarcd-git-credentials")
	if err := os.WriteFile(path, []byte(entry+"\n"), 0o600); err != nil {
		return fmt.Errorf("failed to stage git credentials: %w", err)
	}
	checkout.cleanupPaths = append(checkout.cleanupPaths, path)

	// GIT_CONFIG_* injects the helper without touching any on-disk git config,
	// so two concurrent checkouts never fight over ~/.gitconfig.
	checkout.Env["GIT_CONFIG_COUNT"] = "1"
	checkout.Env["GIT_CONFIG_KEY_0"] = "credential.helper"
	checkout.Env["GIT_CONFIG_VALUE_0"] = "store --file=" + path
	checkout.Env["GIT_TERMINAL_PROMPT"] = "0"
	return nil
}

// credentialStoreEntry renders one line of a git-credential-store file.
func credentialStoreEntry(cloneURL, username, password string) (string, error) {
	parsed, err := url.Parse(cloneURL)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("cannot derive credential host from repository URL %q", cloneURL)
	}
	scheme := parsed.Scheme
	if scheme == "" {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%s@%s", scheme,
		url.QueryEscape(username), url.QueryEscape(password), parsed.Host), nil
}

// Cleanup shreds the credential files staged for a checkout. The working copy
// itself is kept: re-cloning on every Flare would be slow and pointless.
func (w *GitWorkspace) Cleanup(checkout *Checkout) error {
	if checkout == nil {
		return nil
	}
	var firstErr error
	for _, path := range checkout.cleanupPaths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = fmt.Errorf("failed to remove credential file: %w", err)
		}
	}
	checkout.cleanupPaths = nil
	if checkout.credDir != "" {
		if err := os.RemoveAll(checkout.credDir); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("failed to remove the credential staging directory: %w", err)
		}
		checkout.credDir = ""
	}
	return firstErr
}

func (w *GitWorkspace) runner() CommandRunner {
	if w.Runner != nil {
		return w.Runner
	}
	return execCommand
}

// gitEnv renders the checkout environment plus the minimum a git process needs.
func gitEnv(extra map[string]string) []string {
	env := map[string]string{
		"PATH":                os.Getenv("PATH"),
		"HOME":                os.Getenv("HOME"),
		"GIT_TERMINAL_PROMPT": "0",
	}
	for k, v := range extra {
		env[k] = v
	}
	return buildEnv(env)
}

// resolveModuleDir joins the module path onto the repository root and refuses
// anything that escapes it, so a crafted spec cannot point the engine at the
// operator's own filesystem.
func resolveModuleDir(repoDir, modulePath string) (string, error) {
	if modulePath == "" || modulePath == "." {
		return repoDir, nil
	}
	joined := filepath.Join(repoDir, filepath.FromSlash(modulePath))
	rel, err := filepath.Rel(repoDir, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("module path %q escapes the repository root", modulePath)
	}
	if info, err := os.Stat(joined); err != nil || !info.IsDir() {
		return "", fmt.Errorf("module path %q does not exist in the repository", modulePath)
	}
	return joined, nil
}

// ensureTrailingNewline keeps OpenSSH from rejecting a key file that lacks one.
func ensureTrailingNewline(data []byte) []byte {
	if len(data) > 0 && data[len(data)-1] == '\n' {
		return data
	}
	return append(data, '\n')
}

// lastLine returns the final non-empty line of an output blob, which is where
// git puts its failure reason.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if trimmed := strings.TrimSpace(lines[i]); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
