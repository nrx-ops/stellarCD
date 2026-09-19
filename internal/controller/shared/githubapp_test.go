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
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testKey is generated once: a 2048-bit RSA keygen per test case dominates the
// runtime of this file for no added coverage.
var testKey = func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
}()

func pkcs1PEM(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

func pkcs8PEM(t *testing.T, key any) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("failed to marshal PKCS#8 key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func TestParseGitHubRepoURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		apiBase string
		owner   string
		repo    string
		wantErr string
	}{
		{
			name:    "github.com",
			url:     "https://github.com/4sh/cinng-iac",
			apiBase: "https://api.github.com",
			owner:   "4sh",
			repo:    "cinng-iac",
		},
		{
			name:    "git suffix is stripped",
			url:     "https://github.com/4sh/cinng-iac.git",
			apiBase: "https://api.github.com",
			owner:   "4sh",
			repo:    "cinng-iac",
		},
		{
			name:    "host casing is ignored",
			url:     "https://GitHub.com/4sh/cinng-iac",
			apiBase: "https://api.github.com",
			owner:   "4sh",
			repo:    "cinng-iac",
		},
		{
			name:    "enterprise server uses api/v3 on its own host",
			url:     "https://ghe.example.com/4sh/cinng-iac.git",
			apiBase: "https://ghe.example.com/api/v3",
			owner:   "4sh",
			repo:    "cinng-iac",
		},
		{
			name:    "enterprise server keeps a non-default port",
			url:     "https://ghe.example.com:8443/4sh/cinng-iac",
			apiBase: "https://ghe.example.com:8443/api/v3",
			owner:   "4sh",
			repo:    "cinng-iac",
		},
		{
			name:    "ssh scheme is rejected",
			url:     "ssh://git@github.com/4sh/cinng-iac.git",
			wantErr: "HTTP(S) clone URL",
		},
		{
			name:    "scp syntax is rejected",
			url:     "git@github.com:4sh/cinng-iac.git",
			wantErr: "HTTP(S) clone URL",
		},
		{
			name:    "owner without repository is rejected",
			url:     "https://github.com/4sh",
			wantErr: "cannot derive owner and repository",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseGitHubRepoURL(tc.url)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q, got none", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.apiBase != tc.apiBase || got.owner != tc.owner || got.repo != tc.repo {
				t.Fatalf("got %+v, want apiBase=%q owner=%q repo=%q",
					got, tc.apiBase, tc.owner, tc.repo)
			}
		})
	}
}

func TestParseRSAPrivateKey(t *testing.T) {
	edPub, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate an ed25519 key: %v", err)
	}
	_ = edPub

	tests := []struct {
		name    string
		pem     []byte
		wantErr string
	}{
		{name: "pkcs1", pem: pkcs1PEM(t, testKey)},
		{name: "pkcs8", pem: pkcs8PEM(t, testKey)},
		{name: "not pem", pem: []byte("1234567"), wantErr: "not PEM-encoded"},
		{
			name:    "pem but not a key",
			pem:     pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("nonsense")}),
			wantErr: "neither a PKCS#1 nor a PKCS#8",
		},
		{
			name:    "ed25519 is not usable for RS256",
			pem:     pkcs8PEM(t, edPriv),
			wantErr: "not an RSA key",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key, err := parseRSAPrivateKey(tc.pem)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q, got none", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !key.Equal(testKey) {
				t.Fatal("parsed key differs from the one that was encoded")
			}
		})
	}
}

// TestAppJWT checks the assertion GitHub will actually verify: the signature
// must validate against the App public key, and the claims must match what the
// API expects.
func TestAppJWT(t *testing.T) {
	fixed := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	auth := &githubAppAuthenticator{now: func() time.Time { return fixed }}

	assertion, err := auth.appJWT("123456", testKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		t.Fatalf("expected three JWT segments, got %d", len(parts))
	}

	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	decodeSegment(t, parts[0], &header)
	if header.Alg != "RS256" || header.Typ != "JWT" {
		t.Fatalf("got header %+v, want alg=RS256 typ=JWT", header)
	}

	var claims struct {
		Iat int64 `json:"iat"`
		Exp int64 `json:"exp"`
		// Raw, not string: GitHub answers a quoted numeric issuer with
		// "'Issuer' claim ('iss') must be an Integer", so the encoding of this
		// claim is the assertion, not just its value.
		Iss json.RawMessage `json:"iss"`
	}
	decodeSegment(t, parts[1], &claims)
	if got := string(claims.Iss); got != "123456" {
		t.Fatalf("got iss %s, want the unquoted integer 123456", got)
	}
	if want := fixed.Add(-appJWTBackdate).Unix(); claims.Iat != want {
		t.Fatalf("got iat %d, want %d (backdated by %s)", claims.Iat, want, appJWTBackdate)
	}
	if want := fixed.Add(appJWTLifetime).Unix(); claims.Exp != want {
		t.Fatalf("got exp %d, want %d", claims.Exp, want)
	}
	// GitHub rejects an assertion valid for more than ten minutes.
	if claims.Exp-claims.Iat > 600 {
		t.Fatalf("assertion lives %ds, GitHub caps the window at 600s", claims.Exp-claims.Iat)
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("signature is not base64url: %v", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&testKey.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("signature does not verify against the App public key: %v", err)
	}
}

// TestAppJWTClientIDIssuer covers the other accepted issuer: an App Client ID
// is not numeric and must stay a JSON string.
func TestAppJWTClientIDIssuer(t *testing.T) {
	auth := &githubAppAuthenticator{}
	assertion, err := auth.appJWT("Iv23liAbCdEfGhIjKlMn", testKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var claims struct {
		Iss json.RawMessage `json:"iss"`
	}
	decodeSegment(t, strings.Split(assertion, ".")[1], &claims)
	if got := string(claims.Iss); got != `"Iv23liAbCdEfGhIjKlMn"` {
		t.Fatalf("got iss %s, want a quoted client id", got)
	}
}

func decodeSegment(t *testing.T, segment string, out any) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		t.Fatalf("segment is not base64url: %v", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("segment is not JSON: %v", err)
	}
}

// githubStub stands in for the GitHub API and records what it was asked.
type githubStub struct {
	installationCalls int
	tokenCalls        int
	sawAuthHeader     string
	sawAPIVersion     string
}

func (s *githubStub) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/repos/4sh/cinng-iac/installation",
		func(w http.ResponseWriter, r *http.Request) {
			s.installationCalls++
			s.sawAuthHeader = r.Header.Get("Authorization")
			s.sawAPIVersion = r.Header.Get("X-GitHub-Api-Version")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id": 42}`))
		})
	mux.HandleFunc("/api/v3/app/installations/42/access_tokens",
		func(w http.ResponseWriter, r *http.Request) {
			s.tokenCalls++
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token": "ghs_installationtoken", "expires_at": "2026-09-19T13:00:00Z"}`))
		})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestInstallationToken(t *testing.T) {
	stub := &githubStub{}
	srv := stub.server(t)
	auth := &githubAppAuthenticator{client: srv.Client()}

	creds := map[string][]byte{
		SecretKeyGitHubAppID:         []byte("123456"),
		SecretKeyGitHubAppPrivateKey: pkcs1PEM(t, testKey),
	}

	token, err := auth.installationToken(context.Background(), srv.URL+"/4sh/cinng-iac.git", creds)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "ghs_installationtoken" {
		t.Fatalf("got token %q, want the one the API returned", token)
	}
	if stub.installationCalls != 1 || stub.tokenCalls != 1 {
		t.Fatalf("got %d installation and %d token calls, want 1 and 1",
			stub.installationCalls, stub.tokenCalls)
	}
	if !strings.HasPrefix(stub.sawAuthHeader, "Bearer ") {
		t.Fatalf("got Authorization %q, want a Bearer assertion", stub.sawAuthHeader)
	}
	if stub.sawAPIVersion != "2022-11-28" {
		t.Fatalf("got X-GitHub-Api-Version %q, want the pinned version", stub.sawAPIVersion)
	}
}

// TestInstallationTokenPinnedInstallation checks that setting the installation
// id skips discovery, which is the point of exposing that key at all.
func TestInstallationTokenPinnedInstallation(t *testing.T) {
	stub := &githubStub{}
	srv := stub.server(t)
	auth := &githubAppAuthenticator{client: srv.Client()}

	creds := map[string][]byte{
		SecretKeyGitHubAppID:             []byte("123456"),
		SecretKeyGitHubAppPrivateKey:     pkcs1PEM(t, testKey),
		SecretKeyGitHubAppInstallationID: []byte(" 42 "),
	}

	if _, err := auth.installationToken(
		context.Background(), srv.URL+"/4sh/cinng-iac", creds); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stub.installationCalls != 0 {
		t.Fatalf("got %d discovery calls, want none when the id is pinned", stub.installationCalls)
	}
	if stub.tokenCalls != 1 {
		t.Fatalf("got %d token calls, want 1", stub.tokenCalls)
	}
}

func TestInstallationTokenErrors(t *testing.T) {
	validKey := pkcs1PEM(t, testKey)

	tests := []struct {
		name    string
		creds   map[string][]byte
		handler http.HandlerFunc
		wantErr string
	}{
		{
			name:    "missing app id",
			creds:   map[string][]byte{SecretKeyGitHubAppPrivateKey: validKey},
			wantErr: `"githubAppID" is missing`,
		},
		{
			name:    "missing private key",
			creds:   map[string][]byte{SecretKeyGitHubAppID: []byte("123456")},
			wantErr: `"githubAppPrivateKey" is missing`,
		},
		{
			name: "api rejects the assertion",
			creds: map[string][]byte{
				SecretKeyGitHubAppID:         []byte("123456"),
				SecretKeyGitHubAppPrivateKey: validKey,
			},
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"message": "A JSON web token could not be decoded"}`))
			},
			wantErr: "A JSON web token could not be decoded",
		},
		{
			name: "app not installed on the repository",
			creds: map[string][]byte{
				SecretKeyGitHubAppID:         []byte("123456"),
				SecretKeyGitHubAppPrivateKey: validKey,
			},
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			},
			wantErr: "no installation for 4sh/cinng-iac",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := tc.handler
			if handler == nil {
				handler = func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusNotImplemented)
				}
			}
			srv := httptest.NewServer(handler)
			defer srv.Close()

			auth := &githubAppAuthenticator{client: srv.Client()}
			_, err := auth.installationToken(
				context.Background(), srv.URL+"/4sh/cinng-iac", tc.creds)
			if err == nil {
				t.Fatalf("expected an error containing %q, got none", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestMaterialiseCredentialsGitHubApp covers the wiring: an App Secret must end
// up as a 0600 credential-store file holding the minted token, with git pointed
// at it through the environment rather than through a command line.
func TestMaterialiseCredentialsGitHubApp(t *testing.T) {
	stub := &githubStub{}
	srv := stub.server(t)

	repoDir := t.TempDir()
	w := &GitWorkspace{Root: repoDir, HTTPClient: srv.Client()}
	checkout := &Checkout{RepoDir: repoDir, Env: map[string]string{}}

	creds := map[string][]byte{
		SecretKeyGitHubAppID:         []byte("123456"),
		SecretKeyGitHubAppPrivateKey: pkcs1PEM(t, testKey),
	}

	cloneURL := srv.URL + "/4sh/cinng-iac.git"
	if err := w.materialiseCredentials(
		context.Background(), repoDir, cloneURL, creds, checkout); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	path := filepath.Join(repoDir, ".stellarcd-git-credentials")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("credential file was not staged: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("got mode %o, want 0600: the file holds a live token", perm)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read the credential file: %v", err)
	}
	entry := strings.TrimSpace(string(body))
	if !strings.Contains(entry, githubAppUsername) {
		t.Fatalf("entry %q does not use the %q username GitHub documents for App tokens",
			entry, githubAppUsername)
	}
	if !strings.Contains(entry, "ghs_installationtoken") {
		t.Fatal("entry does not carry the minted installation token")
	}

	if got := checkout.Env["GIT_CONFIG_VALUE_0"]; got != "store --file="+path {
		t.Fatalf("got credential helper %q, want it pointed at the staged file", got)
	}
	if got := checkout.Env["GIT_TERMINAL_PROMPT"]; got != "0" {
		t.Fatalf("got GIT_TERMINAL_PROMPT %q, want %q so a failed auth cannot hang", got, "0")
	}
	if len(checkout.cleanupPaths) != 1 || checkout.cleanupPaths[0] != path {
		t.Fatalf("got cleanup paths %v, want the credential file staged for shredding",
			checkout.cleanupPaths)
	}

	if err := w.Cleanup(checkout); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("credential file survived cleanup")
	}
}

// TestMaterialiseCredentialsUnusableSecret pins the behaviour that used to be
// the trap: a Secret with no recognised key must fail loudly instead of letting
// git fall through to an anonymous clone.
func TestMaterialiseCredentialsUnusableSecret(t *testing.T) {
	repoDir := t.TempDir()
	w := &GitWorkspace{Root: repoDir}
	checkout := &Checkout{RepoDir: repoDir, Env: map[string]string{}}

	creds := map[string][]byte{"ghSecret.yaml": []byte("gh-app-id: 123456\n")}

	err := w.materialiseCredentials(
		context.Background(), repoDir, "https://github.com/4sh/cinng-iac", creds, checkout)
	if err == nil {
		t.Fatal("expected an error for a Secret with no usable credential key")
	}
	for _, want := range []string{"sshPrivateKey", "token", "githubAppID"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name the %q key", err, want)
		}
	}
}

// TestMaterialiseCredentialsEmptySecret keeps the no-credentials case working:
// a public repository needs no Secret and must not be turned into an error.
func TestMaterialiseCredentialsEmptySecret(t *testing.T) {
	repoDir := t.TempDir()
	w := &GitWorkspace{Root: repoDir}
	checkout := &Checkout{RepoDir: repoDir, Env: map[string]string{}}

	if err := w.materialiseCredentials(
		context.Background(), repoDir, "https://github.com/4sh/public", nil, checkout); err != nil {
		t.Fatalf("unexpected error for an absent Secret: %v", err)
	}
	if len(checkout.cleanupPaths) != 0 {
		t.Fatalf("got cleanup paths %v, want none", checkout.cleanupPaths)
	}
}
