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
	"strings"
	"time"
)

// AuthMethod names the credential layout a Secret carries. It is reported to
// the dashboard so an operator can see how a repository authenticates without
// being shown any of the material itself.
type AuthMethod string

const (
	// AuthMethodNone means no Secret is referenced, so clones are anonymous.
	AuthMethodNone AuthMethod = "None"
	// AuthMethodSSHKey is an sshPrivateKey entry.
	AuthMethodSSHKey AuthMethod = "SSHKey"
	// AuthMethodGitHubApp is a githubAppID plus githubAppPrivateKey pair.
	AuthMethodGitHubApp AuthMethod = "GitHubApp"
	// AuthMethodToken is a bare token entry.
	AuthMethodToken AuthMethod = "Token"
	// AuthMethodBasicAuth is a username and password pair.
	AuthMethodBasicAuth AuthMethod = "BasicAuth"
	// AuthMethodUnusable means the Secret exists but carries no key the
	// operator understands, which is the one case that fails at clone time
	// rather than at configuration time.
	AuthMethodUnusable AuthMethod = "Unusable"
)

// DetectAuthMethod classifies a credentials Secret using the same precedence
// materialiseCredentials applies, so what the dashboard reports is what the
// clone will actually use.
func DetectAuthMethod(creds map[string][]byte) AuthMethod {
	switch {
	case len(creds) == 0:
		return AuthMethodNone
	case len(creds[SecretKeySSHPrivateKey]) > 0:
		return AuthMethodSSHKey
	case hasGitHubAppCredentials(creds):
		return AuthMethodGitHubApp
	case len(creds[SecretKeyPassword]) > 0:
		return AuthMethodBasicAuth
	case len(creds[SecretKeyToken]) > 0:
		return AuthMethodToken
	default:
		return AuthMethodUnusable
	}
}

// RepositoryState is the outcome of a connectivity check.
type RepositoryState string

const (
	// RepositoryConnected means the remote answered and accepted the credentials.
	RepositoryConnected RepositoryState = "Connected"
	// RepositoryUnauthorized means the remote rejected the credentials.
	RepositoryUnauthorized RepositoryState = "Unauthorized"
	// RepositoryNotFound means the remote denies the repository exists, which
	// for a private repository is indistinguishable from "no access".
	RepositoryNotFound RepositoryState = "NotFound"
	// RepositoryUnreachable means the host could not be contacted.
	RepositoryUnreachable RepositoryState = "Unreachable"
	// RepositoryMisconfigured means the check never left the operator: the
	// Secret is unusable or the App credentials could not be exchanged.
	RepositoryMisconfigured RepositoryState = "Misconfigured"
	// RepositoryUnsupported means there is no way to check this remote over
	// HTTP, which today means an SSH URL.
	RepositoryUnsupported RepositoryState = "Unsupported"
)

// RepositoryStatus is a connectivity verdict safe to hand to an unauthenticated
// dashboard: it names what happened and never quotes credential material or a
// remote response body.
type RepositoryStatus struct {
	State     RepositoryState `json:"state"`
	Message   string          `json:"message"`
	CheckedAt time.Time       `json:"checkedAt"`
	// AuthMethod is how the check authenticated, or would have.
	AuthMethod AuthMethod `json:"authMethod"`
}

// probeTimeout bounds one connectivity check. The dashboard polls, so a slow
// remote must not pile up requests.
const probeTimeout = 10 * time.Second

// probeClient never follows a redirect into a different auth context: git hosts
// redirect /repo to /repo/ and to canonical casing, and blindly replaying basic
// auth across a redirect would leak the token to whatever host answered.
var probeClient = &http.Client{
	Timeout: probeTimeout,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) == 0 {
			return nil
		}
		if req.URL.Host != via[0].URL.Host || req.URL.Scheme != via[0].URL.Scheme {
			return http.ErrUseLastResponse
		}
		if len(via) >= 3 {
			return http.ErrUseLastResponse
		}
		return nil
	},
}

// CheckRepository reports whether the operator can reach and authenticate to a
// repository, using the credentials a Flare would use.
//
// It speaks git's smart HTTP discovery endpoint rather than shelling out to
// git, for two reasons: the controller image has no git binary, and a ref
// advertisement is a cheap read that never writes to disk.
func CheckRepository(ctx context.Context, cloneURL string, creds map[string][]byte) RepositoryStatus {
	method := DetectAuthMethod(creds)
	now := time.Now()
	status := RepositoryStatus{AuthMethod: method, CheckedAt: now}

	switch method {
	case AuthMethodUnusable:
		status.State = RepositoryMisconfigured
		status.Message = fmt.Sprintf(
			"the referenced Secret carries none of %q, %q, %q/%q or %q plus %q",
			SecretKeySSHPrivateKey, SecretKeyToken, SecretKeyUsername, SecretKeyPassword,
			SecretKeyGitHubAppID, SecretKeyGitHubAppPrivateKey)
		return status
	case AuthMethodSSHKey:
		status.State = RepositoryUnsupported
		status.Message = "SSH credentials cannot be verified over HTTP; the check is skipped"
		return status
	}

	parsed, err := url.Parse(cloneURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		status.State = RepositoryUnsupported
		status.Message = "only http and https clone URLs can be checked"
		return status
	}

	username, password, err := probeCredentials(ctx, cloneURL, creds, method)
	if err != nil {
		status.State = RepositoryMisconfigured
		status.Message = err.Error()
		return status
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, refsEndpoint(parsed), nil)
	if err != nil {
		status.State = RepositoryUnreachable
		status.Message = "failed to build the probe request"
		return status
	}
	if password != "" {
		req.SetBasicAuth(username, password)
	}
	// Asking for the smart protocol keeps the answer to a ref advertisement
	// instead of a dumb-HTTP directory listing.
	req.Header.Set("User-Agent", "git/2.0 (stellarcd)")

	resp, err := probeClient.Do(req)
	if err != nil {
		status.State = RepositoryUnreachable
		// The URL is already known to the caller; the transport error may quote
		// it back with credentials attached, so only the host is reported.
		status.Message = fmt.Sprintf("could not reach %s", parsed.Host)
		return status
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusOK:
		status.State = RepositoryConnected
		status.Message = "the remote accepted the credentials and advertised its refs"
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		status.State = RepositoryUnauthorized
		status.Message = fmt.Sprintf("the remote rejected the credentials (%s)", resp.Status)
	case resp.StatusCode == http.StatusNotFound:
		status.State = RepositoryNotFound
		status.Message = "the remote reports no such repository, or the credentials cannot see it"
	default:
		status.State = RepositoryUnreachable
		status.Message = fmt.Sprintf("the remote answered %s", resp.Status)
	}
	return status
}

// refsEndpoint builds the smart HTTP ref-advertisement URL for a clone URL.
func refsEndpoint(parsed *url.URL) string {
	endpoint := *parsed
	endpoint.User = nil
	endpoint.Path = strings.TrimSuffix(endpoint.Path, "/") + "/info/refs"
	endpoint.RawQuery = "service=git-upload-pack"
	return endpoint.String()
}

// probeCredentials resolves the username and password the probe should present.
// A GitHub App is exchanged for an installation token here, which means the
// check exercises the same code path a real clone would.
func probeCredentials(
	ctx context.Context,
	cloneURL string,
	creds map[string][]byte,
	method AuthMethod,
) (string, string, error) {
	switch method {
	case AuthMethodNone:
		return "", "", nil
	case AuthMethodGitHubApp:
		auth := &githubAppAuthenticator{}
		token, err := auth.installationToken(ctx, cloneURL, creds)
		if err != nil {
			return "", "", err
		}
		return githubAppUsername, token, nil
	default:
		password := string(creds[SecretKeyPassword])
		if password == "" {
			password = string(creds[SecretKeyToken])
		}
		username := string(creds[SecretKeyUsername])
		if username == "" {
			username = "stellarcd"
		}
		return username, password, nil
	}
}
