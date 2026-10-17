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
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nrx-ops/stellarCD/internal/controller/shared"
)

// defaultBranch is checked out when the request names no branch.
const defaultBranch = "main"

// CheckoutRequest describes the working copy a Flare needs.
type CheckoutRequest struct {
	// Key uniquely identifies the checkout on disk, e.g. "<universe>/<galaxy>/<astral>".
	Key string
	// URL is the clone URL.
	URL string
	// Branch is the branch to check out.
	Branch string
	// Revision pins a commit on that branch. Empty means its tip.
	Revision string
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
	// Runner defaults to a Shell that discards its output when nil.
	Runner CommandRunner
}

// NewGitWorkspace returns a provider rooted at root.
func NewGitWorkspace(root string) *GitWorkspace {
	return &GitWorkspace{Root: root, Runner: (&Shell{}).Run}
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
	if err := w.materialiseCredentials(checkout.credDir, req.URL, req.Credentials, checkout); err != nil {
		_ = w.Cleanup(checkout)
		return nil, err
	}

	branch := req.Branch
	if branch == "" {
		branch = defaultBranch
	}
	if err := w.sync(ctx, repoDir, req.URL, branch, checkout); err != nil {
		_ = w.Cleanup(checkout)
		return nil, err
	}
	if req.Revision != "" {
		if err := w.pin(ctx, repoDir, req.Revision, checkout); err != nil {
			_ = w.Cleanup(checkout)
			return nil, err
		}
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
func (w *GitWorkspace) sync(ctx context.Context, repoDir, remoteURL, branch string, checkout *Checkout) error {
	env := gitEnv(checkout.Env)
	runner := w.runner()

	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		out, _, cloneErr := runner(ctx, repoDir, env, "git",
			"clone", "--depth=1", "--branch", branch, "--single-branch", remoteURL, ".")
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

// pin moves the checkout to one commit. A shallow clone only has the branch
// tip, so the commit is fetched by SHA first; GitHub, GitLab and Gitea all
// allow that for reachable commits.
func (w *GitWorkspace) pin(ctx context.Context, repoDir, revision string, checkout *Checkout) error {
	env := gitEnv(checkout.Env)
	runner := w.runner()
	if out, _, err := runner(ctx, repoDir, env, "git", "fetch", "--depth=1", "origin", revision); err != nil {
		return fmt.Errorf("failed to fetch revision %s: %w: %s", revision, err, lastLine(out))
	}
	if out, _, err := runner(ctx, repoDir, env, "git", "checkout", "--detach", revision); err != nil {
		return fmt.Errorf("failed to check out revision %s: %w: %s", revision, err, lastLine(out))
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
// Exactly one layout is used per checkout: an SSH key, else a static
// username/password or token. A GitHub App never reaches this point: the
// controller exchanges it for an installation token before the Job starts, so
// the App's private key stays out of a pod that runs repository code. A Secret
// carrying none of these is an error rather than a silent anonymous clone,
// which would fail much later with a message about the repository instead of
// the Secret.
func (w *GitWorkspace) materialiseCredentials(
	credDir, cloneURL string,
	creds map[string][]byte,
	checkout *Checkout,
) error {
	if len(creds) == 0 {
		return nil
	}

	if key, ok := creds[shared.SecretKeySSHPrivateKey]; ok && len(key) > 0 {
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

	if shared.IsGitHubApp(creds) {
		return errors.New("GitHub App credentials reached the runner; " +
			"the controller must exchange them for an installation token")
	}

	password := string(creds[shared.SecretKeyPassword])
	if password == "" {
		password = string(creds[shared.SecretKeyToken])
	}
	if password == "" {
		return fmt.Errorf(
			"VCS credentials Secret carries none of %q, %q, %q/%q or %q plus %q",
			shared.SecretKeySSHPrivateKey, shared.SecretKeyToken, shared.SecretKeyUsername,
			shared.SecretKeyPassword, shared.SecretKeyGitHubAppID, shared.SecretKeyGitHubAppPrivateKey)
	}
	username := string(creds[shared.SecretKeyUsername])
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
	return (&Shell{}).Run
}

// gitEnv renders the checkout environment plus the minimum a git process needs.
func gitEnv(extra map[string]string) []string {
	env := map[string]string{
		"PATH":                os.Getenv("PATH"),
		"HOME":                os.Getenv("HOME"),
		"GIT_TERMINAL_PROMPT": "0",
	}
	maps.Copy(env, extra)
	return renderEnv(env)
}

// renderEnv renders an environment map in a stable order. Git gets only what
// is listed here: the runner's own environment carries the cloud credentials,
// which a git process has no business seeing.
func renderEnv(env map[string]string) []string {
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
	for _, v := range slices.Backward(lines) {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
