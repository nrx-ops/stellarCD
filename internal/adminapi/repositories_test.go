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

package adminapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
	"github.com/nrx-ops/stellarCD/internal/controller/shared"
)

func galaxy(namespace, name, secret string) *corev1alpha1.Galaxy {
	g := &corev1alpha1.Galaxy{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       corev1alpha1.GalaxySpec{UniverseRef: "u"},
	}
	if secret != "" {
		g.Spec.VCSSecretRef = &corev1alpha1.SecretRef{Name: secret}
	}
	return g
}

func astral(namespace, name, galaxyRef, url, secret string) *corev1alpha1.Astral {
	a := &corev1alpha1.Astral{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: corev1alpha1.AstralSpec{
			UniverseRef: "u",
			GalaxyRef:   galaxyRef,
			VCSRepository: corev1alpha1.VCSRepository{
				Provider: corev1alpha1.VCSProviderGitHub,
				URL:      url,
				Branch:   "main",
				Path:     ".",
			},
		},
	}
	if secret != "" {
		a.Spec.VCSRepository.SecretRef = &corev1alpha1.SecretRef{Name: secret}
	}
	return a
}

func secret(namespace, name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, ResourceVersion: "1"},
		Data:       data,
	}
}

// TestListRepositoriesInventory checks the flattening: where each repository
// came from, and which Secret it ends up using.
func TestListRepositoriesInventory(t *testing.T) {
	s := newServer(t,
		galaxy("tenant", "gal", "galaxy-creds"),
		astral("tenant", "inherits", "gal", "https://example.invalid/a.git", ""),
		astral("tenant", "overrides", "gal", "https://example.invalid/b.git", "own-creds"),
		secret("tenant", "galaxy-creds", map[string][]byte{shared.SecretKeyToken: []byte("t")}),
		secret("tenant", "own-creds", map[string][]byte{shared.SecretKeyToken: []byte("t")}),
	)

	rec := do(t, s, "/api/v1/repositories?check=false")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	repos := decode[[]RepositoryInfo](t, rec)
	if len(repos) != 2 {
		t.Fatalf("got %d repositories, want 2", len(repos))
	}

	byName := map[string]RepositoryInfo{}
	for _, r := range repos {
		byName[r.Name] = r
	}

	if got := byName["inherits"]; got.SecretName != "galaxy-creds" || got.SecretScope != "Galaxy" {
		t.Fatalf("got secret %q scope %q, want the Galaxy's credentials inherited",
			got.SecretName, got.SecretScope)
	}
	if got := byName["overrides"]; got.SecretName != "own-creds" || got.SecretScope != "Astral" {
		t.Fatalf("got secret %q scope %q, want the Astral's own credentials",
			got.SecretName, got.SecretScope)
	}
	if got := byName["inherits"]; got.Kind != "Astral" || got.Galaxy != "gal" || got.Branch != "main" {
		t.Fatalf("got %+v, want the Astral coordinates carried through", got)
	}
}

// TestListRepositoriesNeverLeaksCredentials is the important one: the dashboard
// in front of this endpoint is unauthenticated, so no byte of a Secret may
// appear in the response.
func TestListRepositoriesNeverLeaksCredentials(t *testing.T) {
	const password = "sup3rs3cr3t-should-never-be-serialised"
	s := newServer(t,
		galaxy("tenant", "gal", "creds"),
		astral("tenant", "app", "gal", "https://example.invalid/a.git", ""),
		secret("tenant", "creds", map[string][]byte{
			shared.SecretKeyUsername: []byte("alice"),
			shared.SecretKeyPassword: []byte(password),
		}),
	)

	rec := do(t, s, "/api/v1/repositories")
	body := rec.Body.String()
	for _, forbidden := range []string{password, "alice"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked credential material %q: %s", forbidden, body)
		}
	}

	repos := decode[[]RepositoryInfo](t, rec)
	if len(repos) != 1 {
		t.Fatalf("got %d repositories, want 1", len(repos))
	}
	// The auth method is reported; the material behind it is not.
	if repos[0].Connection.AuthMethod != shared.AuthMethodBasicAuth {
		t.Fatalf("got auth method %q, want %q",
			repos[0].Connection.AuthMethod, shared.AuthMethodBasicAuth)
	}
}

// TestListRepositoriesMissingSecret separates "the Secret is not there" from
// "the credentials were rejected"; they need different fixes.
func TestListRepositoriesMissingSecret(t *testing.T) {
	s := newServer(t,
		galaxy("tenant", "gal", "absent"),
		astral("tenant", "app", "gal", "https://example.invalid/a.git", ""),
	)

	repos := decode[[]RepositoryInfo](t, do(t, s, "/api/v1/repositories"))
	if len(repos) != 1 {
		t.Fatalf("got %d repositories, want 1", len(repos))
	}
	if !repos[0].SecretMissing {
		t.Fatal("secretMissing was not set for an absent Secret")
	}
	if repos[0].Connection.State != shared.RepositoryMisconfigured {
		t.Fatalf("got state %q, want %q", repos[0].Connection.State, shared.RepositoryMisconfigured)
	}
}

// TestListRepositoriesConnects drives a real HTTP remote so the Connected
// verdict is produced by an actual ref advertisement, not a stub.
func TestListRepositoriesConnects(t *testing.T) {
	var sawAuth string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repo.git/info/refs" || r.URL.Query().Get("service") != "git-upload-pack" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = w.Write([]byte("001e# service=git-upload-pack\n"))
	}))
	defer remote.Close()

	s := newServer(t,
		galaxy("tenant", "gal", "creds"),
		astral("tenant", "app", "gal", remote.URL+"/repo.git", ""),
		secret("tenant", "creds", map[string][]byte{shared.SecretKeyToken: []byte("ghp_x")}),
	)

	repos := decode[[]RepositoryInfo](t, do(t, s, "/api/v1/repositories"))
	if repos[0].Connection.State != shared.RepositoryConnected {
		t.Fatalf("got state %q (%s), want %q",
			repos[0].Connection.State, repos[0].Connection.Message, shared.RepositoryConnected)
	}
	if sawAuth == "" {
		t.Fatal("the probe did not present credentials")
	}
}

// TestListRepositoriesUnauthorized covers the verdict an operator will hit most
// often: right repository, wrong or expired credentials.
func TestListRepositoriesUnauthorized(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer remote.Close()

	s := newServer(t,
		galaxy("tenant", "gal", "creds"),
		astral("tenant", "app", "gal", remote.URL+"/repo.git", ""),
		secret("tenant", "creds", map[string][]byte{shared.SecretKeyToken: []byte("stale")}),
	)

	repos := decode[[]RepositoryInfo](t, do(t, s, "/api/v1/repositories"))
	if repos[0].Connection.State != shared.RepositoryUnauthorized {
		t.Fatalf("got state %q, want %q", repos[0].Connection.State, shared.RepositoryUnauthorized)
	}
}

// TestRepositoryProbeIsCached keeps a polling dashboard from re-minting a token
// and re-hitting the remote on every refresh.
func TestRepositoryProbeIsCached(t *testing.T) {
	var hits int
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer remote.Close()

	s := newServer(t,
		galaxy("tenant", "gal", "creds"),
		astral("tenant", "app", "gal", remote.URL+"/repo.git", ""),
		secret("tenant", "creds", map[string][]byte{shared.SecretKeyToken: []byte("t")}),
	)

	for i := 0; i < 4; i++ {
		do(t, s, "/api/v1/repositories")
	}
	if hits != 1 {
		t.Fatalf("remote was hit %d times, want 1: the verdict should be cached", hits)
	}
}

// TestRepositorySSHIsNotChecked records that an SSH remote is reported as
// unsupported rather than silently shown as broken.
func TestRepositorySSHIsNotChecked(t *testing.T) {
	s := newServer(t,
		galaxy("tenant", "gal", "creds"),
		astral("tenant", "app", "gal", "ssh://git@example.invalid/a.git", ""),
		secret("tenant", "creds", map[string][]byte{
			shared.SecretKeySSHPrivateKey: []byte("-----BEGIN OPENSSH PRIVATE KEY-----"),
		}),
	)

	repos := decode[[]RepositoryInfo](t, do(t, s, "/api/v1/repositories"))
	if repos[0].Connection.State != shared.RepositoryUnsupported {
		t.Fatalf("got state %q, want %q", repos[0].Connection.State, shared.RepositoryUnsupported)
	}
	if repos[0].Connection.AuthMethod != shared.AuthMethodSSHKey {
		t.Fatalf("got auth method %q, want %q",
			repos[0].Connection.AuthMethod, shared.AuthMethodSSHKey)
	}
}
