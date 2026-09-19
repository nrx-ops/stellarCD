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
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Well-known keys read from a GitHub App credentials Secret.
const (
	// SecretKeyGitHubAppID is the numeric App ID from the App settings page.
	SecretKeyGitHubAppID = "githubAppID"
	// SecretKeyGitHubAppPrivateKey is the PEM private key generated for the App.
	SecretKeyGitHubAppPrivateKey = "githubAppPrivateKey"
	// SecretKeyGitHubAppInstallationID pins the installation to mint a token
	// for. When absent it is discovered from the repository, which costs one
	// extra API call but survives the App being reinstalled.
	SecretKeyGitHubAppInstallationID = "githubAppInstallationID"
)

const (
	// githubAppUsername is the username GitHub documents for installation
	// access tokens over HTTPS. The token goes in the password field.
	githubAppUsername = "x-access-token"

	// appJWTLifetime is how long the App-level assertion stays valid. GitHub
	// rejects anything over ten minutes; nine leaves room for clock skew.
	appJWTLifetime = 9 * time.Minute

	// appJWTBackdate moves iat into the past so a slightly fast operator clock
	// does not produce an assertion GitHub considers issued in the future.
	appJWTBackdate = 60 * time.Second

	// maxGitHubResponseBytes caps what is read from an API response. The bodies
	// involved are a few hundred bytes; the limit only exists so a misrouted
	// endpoint cannot stream unbounded data into the operator.
	maxGitHubResponseBytes = 1 << 20
)

// defaultGitHubHTTPClient bounds the whole exchange. Without a timeout a hung
// API call would block a Flare until its context deadline, which may be the
// long Terraform timeout rather than anything appropriate for one HTTP call.
var defaultGitHubHTTPClient = &http.Client{Timeout: 30 * time.Second}

// githubAppAuthenticator exchanges a GitHub App private key for a short-lived
// installation access token.
//
// Nothing is cached. Tokens last an hour and one is minted per checkout, so a
// cache would have to be invalidated on expiry, on key rotation and on the App
// being reinstalled, for no measurable gain at this call rate.
type githubAppAuthenticator struct {
	// client defaults to defaultGitHubHTTPClient when nil.
	client *http.Client
	// now defaults to time.Now when nil. Tests pin it to assert the claims.
	now func() time.Time
}

func (a *githubAppAuthenticator) httpClient() *http.Client {
	if a.client != nil {
		return a.client
	}
	return defaultGitHubHTTPClient
}

func (a *githubAppAuthenticator) timeNow() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// hasGitHubAppCredentials reports whether a Secret carries App fields. It
// matches on either field so that a half-filled Secret is reported as an
// incomplete App configuration rather than silently ignored.
func hasGitHubAppCredentials(creds map[string][]byte) bool {
	return len(creds[SecretKeyGitHubAppID]) > 0 || len(creds[SecretKeyGitHubAppPrivateKey]) > 0
}

// githubRepo locates a repository and the API endpoint that serves it.
type githubRepo struct {
	apiBase string
	owner   string
	repo    string
}

// installationToken exchanges the App credentials for an installation access
// token scoped to the repository behind cloneURL.
func (a *githubAppAuthenticator) installationToken(
	ctx context.Context,
	cloneURL string,
	creds map[string][]byte,
) (string, error) {
	appID := strings.TrimSpace(string(creds[SecretKeyGitHubAppID]))
	if appID == "" {
		return "", fmt.Errorf("GitHub App credentials are incomplete: %q is missing", SecretKeyGitHubAppID)
	}
	if len(creds[SecretKeyGitHubAppPrivateKey]) == 0 {
		return "", fmt.Errorf("GitHub App credentials are incomplete: %q is missing",
			SecretKeyGitHubAppPrivateKey)
	}
	key, err := parseRSAPrivateKey(creds[SecretKeyGitHubAppPrivateKey])
	if err != nil {
		return "", err
	}
	target, err := parseGitHubRepoURL(cloneURL)
	if err != nil {
		return "", err
	}
	assertion, err := a.appJWT(appID, key)
	if err != nil {
		return "", err
	}

	installationID := strings.TrimSpace(string(creds[SecretKeyGitHubAppInstallationID]))
	if installationID == "" {
		installationID, err = a.discoverInstallation(ctx, target, assertion)
		if err != nil {
			return "", err
		}
	}
	return a.mintToken(ctx, target, installationID, assertion)
}

// parseGitHubRepoURL splits a clone URL into its owner/repo pair and the API
// base serving it. github.com is served by api.github.com; any other host is
// treated as GitHub Enterprise Server, whose API lives under /api/v3.
func parseGitHubRepoURL(cloneURL string) (githubRepo, error) {
	// scp-style remotes such as git@github.com:owner/repo.git are the usual way
	// an SSH remote is written, and url.Parse rejects them with a message about
	// colons in path segments. Catch the shape first so the user is told the
	// real problem: App tokens do not authenticate SSH.
	if !strings.Contains(cloneURL, "://") && strings.Contains(cloneURL, ":") {
		return githubRepo{}, unsupportedSchemeError(cloneURL)
	}
	parsed, err := url.Parse(cloneURL)
	if err != nil {
		return githubRepo{}, fmt.Errorf("cannot parse repository URL %q: %w", cloneURL, err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return githubRepo{}, unsupportedSchemeError(cloneURL)
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) < 2 || segments[0] == "" || segments[1] == "" {
		return githubRepo{}, fmt.Errorf("cannot derive owner and repository from %q", cloneURL)
	}

	target := githubRepo{
		owner:   segments[0],
		repo:    strings.TrimSuffix(segments[1], ".git"),
		apiBase: parsed.Scheme + "://" + parsed.Host + "/api/v3",
	}
	if host := strings.ToLower(parsed.Hostname()); host == "github.com" || host == "www.github.com" {
		target.apiBase = "https://api.github.com"
	}
	return target, nil
}

// unsupportedSchemeError explains why an SSH remote cannot use App credentials,
// and points at the two ways out rather than just refusing.
func unsupportedSchemeError(cloneURL string) error {
	return fmt.Errorf(
		"GitHub App credentials need an HTTP(S) clone URL, got %q: an installation token "+
			"authenticates an HTTPS remote, not an SSH one. Either switch the repository URL "+
			"to https://, or put a deploy key in %q instead", cloneURL, SecretKeySSHPrivateKey)
}

// parseRSAPrivateKey accepts both PEM layouts GitHub has issued: PKCS#1, which
// the App settings page generates today, and PKCS#8, which is what a key run
// through openssl usually comes back as.
func parseRSAPrivateKey(keyPEM []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("%q is not PEM-encoded", SecretKeyGitHubAppPrivateKey)
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%q is neither a PKCS#1 nor a PKCS#8 private key: %w",
			SecretKeyGitHubAppPrivateKey, err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%q is not an RSA key, and GitHub Apps sign with RS256",
			SecretKeyGitHubAppPrivateKey)
	}
	return key, nil
}

// appJWT builds the RS256 assertion that authenticates as the App itself.
//
// It is hand-rolled rather than pulled from a JWT library: one fixed algorithm
// and three claims do not justify a dependency, and every primitive involved is
// already in the standard library. Fixing alg to RS256 at the call site also
// means there is no algorithm-negotiation path to get wrong.
func (a *githubAppAuthenticator) appJWT(appID string, key *rsa.PrivateKey) (string, error) {
	now := a.timeNow()
	headerJSON, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", fmt.Errorf("failed to encode JWT header: %w", err)
	}
	claims := map[string]any{
		"iat": now.Add(-appJWTBackdate).Unix(),
		"exp": now.Add(appJWTLifetime).Unix(),
		"iss": appID,
	}
	// A numeric App ID must be encoded as a JSON number: GitHub answers a
	// quoted one with "'Issuer' claim ('iss') must be an Integer". An App's
	// Client ID is also accepted as the issuer and is not numeric, so anything
	// that does not parse as an integer is left as a string.
	if id, convErr := strconv.ParseInt(appID, 10, 64); convErr == nil {
		claims["iss"] = id
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("failed to encode JWT claims: %w", err)
	}

	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(headerJSON) + "." + enc.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("failed to sign the App assertion: %w", err)
	}
	return signingInput + "." + enc.EncodeToString(signature), nil
}

// discoverInstallation asks which installation of the App can see the
// repository. Setting githubAppInstallationID skips this round trip.
func (a *githubAppAuthenticator) discoverInstallation(
	ctx context.Context,
	target githubRepo,
	assertion string,
) (string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/installation", target.apiBase,
		url.PathEscape(target.owner), url.PathEscape(target.repo))

	var payload struct {
		ID int64 `json:"id"`
	}
	if err := a.call(ctx, http.MethodGet, endpoint, assertion, &payload); err != nil {
		return "", fmt.Errorf("failed to resolve the App installation for %s/%s: %w",
			target.owner, target.repo, err)
	}
	if payload.ID == 0 {
		return "", fmt.Errorf(
			"GitHub reported no installation for %s/%s: is the App installed on that repository?",
			target.owner, target.repo)
	}
	return strconv.FormatInt(payload.ID, 10), nil
}

// mintToken trades the App assertion for an installation access token.
func (a *githubAppAuthenticator) mintToken(
	ctx context.Context,
	target githubRepo,
	installationID, assertion string,
) (string, error) {
	endpoint := fmt.Sprintf("%s/app/installations/%s/access_tokens",
		target.apiBase, url.PathEscape(installationID))

	var payload struct {
		Token string `json:"token"`
	}
	if err := a.call(ctx, http.MethodPost, endpoint, assertion, &payload); err != nil {
		return "", fmt.Errorf("failed to mint an installation access token: %w", err)
	}
	if payload.Token == "" {
		return "", errors.New("GitHub returned an empty installation access token")
	}
	return payload.Token, nil
}

// call performs one assertion-authenticated API request and decodes the fields
// the caller asked for. Neither the assertion nor the response body is ever
// placed in an error: the success body carries a live token, and the error body
// echoes request details back.
func (a *githubAppAuthenticator) call(
	ctx context.Context,
	method, endpoint, assertion string,
	out any,
) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to build the request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+assertion)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := a.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("request to %s failed: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGitHubResponseBytes))
	if err != nil {
		return fmt.Errorf("failed to read the response from %s: %w", endpoint, err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("%s returned %s: %s", endpoint, resp.Status, githubErrorMessage(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("failed to decode the response from %s: %w", endpoint, err)
	}
	return nil
}

// githubErrorMessage pulls the human-readable reason out of an API error body.
// Only that one field is surfaced; the rest of the payload is request detail
// echoed back and does not belong in an error a user reads.
func githubErrorMessage(body []byte) string {
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.Message != "" {
		return payload.Message
	}
	return "no message in the response body"
}
