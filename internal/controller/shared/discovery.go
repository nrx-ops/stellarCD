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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"unicode"
)

// maxTreeBytes caps the tree response. A repository with a six-figure file
// count produces a large document, and the operator should fail loudly on it
// rather than grow its heap until the pod is OOM-killed.
const maxTreeBytes = 32 << 20

// Module is one discovered root module: a directory the engine can run in.
type Module struct {
	// Path is the directory, relative to the repository root. "." is the root.
	Path string
	// Marker is the file that identified it, e.g. "terragrunt.hcl".
	Marker string
}

// RepositoryTree is a flat listing of a repository at one revision.
type RepositoryTree struct {
	// Files are every blob path, relative to the repository root.
	Files []string
	// Truncated reports that the host refused to list the whole tree. Results
	// derived from a truncated tree are incomplete, and saying so is the
	// difference between "this repository has 40 modules" and "we saw 40".
	Truncated bool
}

// ListRepositoryTree fetches the file listing of a repository at a branch.
//
// It uses the host's tree API rather than a clone: the controller image ships
// no git binary, and a recursive tree is one request against data the host
// already has indexed, where a clone would move the whole history over the
// network just to read the directory names.
func ListRepositoryTree(
	ctx context.Context,
	cloneURL, branch string,
	creds map[string][]byte,
) (RepositoryTree, error) {
	if branch == "" {
		branch = "main"
	}
	target, err := parseGitHubRepoURL(cloneURL)
	if err != nil {
		return RepositoryTree{}, err
	}

	username, password, err := probeCredentials(ctx, cloneURL, creds, DetectAuthMethod(creds))
	if err != nil {
		return RepositoryTree{}, err
	}

	endpoint := fmt.Sprintf("%s/repos/%s/%s/git/trees/%s?recursive=1",
		target.apiBase, url.PathEscape(target.owner), url.PathEscape(target.repo),
		url.PathEscape(branch))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return RepositoryTree{}, fmt.Errorf("failed to build the tree request: %w", err)
	}
	if password != "" {
		req.SetBasicAuth(username, password)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := probeClient.Do(req)
	if err != nil {
		return RepositoryTree{}, fmt.Errorf("could not reach %s", target.apiBase)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTreeBytes))
	if err != nil {
		return RepositoryTree{}, fmt.Errorf("failed to read the tree response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return RepositoryTree{}, fmt.Errorf("listing %s/%s@%s returned %s: %s",
			target.owner, target.repo, branch, resp.Status, githubErrorMessage(body))
	}

	var payload struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return RepositoryTree{}, fmt.Errorf("failed to decode the tree response: %w", err)
	}

	tree := RepositoryTree{Truncated: payload.Truncated}
	for _, entry := range payload.Tree {
		if entry.Type == "blob" {
			tree.Files = append(tree.Files, entry.Path)
		}
	}
	return tree, nil
}

// engineMarkers returns the filenames that identify a root module for an
// engine. Terragrunt units are declared by their own file; Terraform and
// OpenTofu are identified by any .tf file in the directory, handled below.
func engineMarker(engine string) string {
	if strings.EqualFold(engine, "Terragrunt") {
		return "terragrunt.hcl"
	}
	return ".tf"
}

// DetectModules groups a file listing into root module directories for an
// engine.
//
// A Terragrunt unit is a directory holding terragrunt.hcl. A Terraform or
// OpenTofu root module is a directory holding at least one .tf file. Neither
// rule can tell a root module from a reusable child module by looking at file
// names alone, which is what the Include and Exclude patterns are for.
func DetectModules(files []string, engine string) []Module {
	marker := engineMarker(engine)
	seen := map[string]string{}

	for _, file := range files {
		base := path.Base(file)
		dir := path.Dir(file)

		switch {
		case marker == "terragrunt.hcl":
			if base != "terragrunt.hcl" {
				continue
			}
		default:
			if !strings.HasSuffix(base, ".tf") {
				continue
			}
		}
		// A directory is recorded once, by the first marker that named it, so
		// a module with twenty .tf files still yields one Module.
		if _, ok := seen[dir]; !ok {
			seen[dir] = base
		}
	}

	modules := make([]Module, 0, len(seen))
	for dir, base := range seen {
		modules = append(modules, Module{Path: dir, Marker: base})
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].Path < modules[j].Path })
	return modules
}

// MatchesAny reports whether a module directory matches any of the patterns.
//
// A pattern matches three ways, in increasing looseness: it equals the
// directory, it names a parent directory of it, or it matches as a shell glob.
// The parent-prefix rule is what makes the obvious thing work, since writing
// "terragrunt/environments/test" and getting only that exact directory would
// surprise everyone.
func MatchesAny(dir string, patterns []string) bool {
	for _, pattern := range patterns {
		pattern = strings.TrimSuffix(strings.TrimSpace(pattern), "/")
		if pattern == "" {
			continue
		}
		if pattern == dir || strings.HasPrefix(dir, pattern+"/") {
			return true
		}
		if ok, err := path.Match(pattern, dir); err == nil && ok {
			return true
		}
	}
	return false
}

// FilterModules applies the include and exclude patterns. An empty include list
// keeps everything; exclude always wins.
func FilterModules(modules []Module, include, exclude []string) []Module {
	out := make([]Module, 0, len(modules))
	for _, m := range modules {
		if len(include) > 0 && !MatchesAny(m.Path, include) {
			continue
		}
		if MatchesAny(m.Path, exclude) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// maxNameLength bounds a derived object name. Kubernetes allows 253 for a
// subdomain name, but an Astral name also becomes part of label values and log
// lines, so it is kept to the 63-character label limit.
const maxNameLength = 63

// hashSuffixLength is how much of the path digest is appended when a name has
// to be shortened or de-duplicated.
const hashSuffixLength = 8

// ModuleName derives a stable, DNS-1123 object name from a module directory.
//
// The name has to be deterministic: a second discovery pass over an unchanged
// repository must produce the same names, or every pass would delete and
// recreate every Astral.
func ModuleName(dir, stripPrefix string) string {
	trimmed := dir
	if stripPrefix != "" {
		prefix := strings.TrimSuffix(stripPrefix, "/") + "/"
		trimmed = strings.TrimPrefix(trimmed, prefix)
		trimmed = strings.TrimPrefix(trimmed, strings.TrimSuffix(stripPrefix, "/"))
	}
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" || trimmed == "." {
		trimmed = "root"
	}

	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(trimmed) {
		switch {
		case unicode.IsLetter(r) && r < unicode.MaxASCII, unicode.IsDigit(r) && r < unicode.MaxASCII:
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "module"
	}

	if len(name) > maxNameLength {
		// Truncating alone would collide for two long sibling paths, so the
		// digest of the full path is what keeps the result unique.
		keep := maxNameLength - hashSuffixLength - 1
		name = strings.Trim(name[:keep], "-") + "-" + pathDigest(dir)
	}
	return name
}

// pathDigest is a short, stable digest of a module path.
func pathDigest(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	return hex.EncodeToString(sum[:])[:hashSuffixLength]
}

// NameModules assigns every module a unique name. Two different directories can
// sanitise to the same string, so collisions are broken with the path digest
// rather than with a counter, which would reshuffle names as modules come and
// go.
func NameModules(modules []Module, stripPrefix string) map[string]string {
	names := make(map[string]string, len(modules))
	taken := make(map[string]string, len(modules))

	for _, m := range modules {
		name := ModuleName(m.Path, stripPrefix)
		if owner, clash := taken[name]; clash && owner != m.Path {
			keep := name
			if len(keep) > maxNameLength-hashSuffixLength-1 {
				keep = strings.Trim(keep[:maxNameLength-hashSuffixLength-1], "-")
			}
			name = keep + "-" + pathDigest(m.Path)
		}
		taken[name] = m.Path
		names[m.Path] = name
	}
	return names
}
