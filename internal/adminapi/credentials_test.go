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
	"context"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
	"github.com/nrx-ops/stellarCD/internal/controller/shared"
)

// secretValue is planted in every fixture Secret: no answer may contain it.
const secretValue = "v4lue-that-must-never-leave-the-cluster"

// credential is a Secret of the tenant namespace holding secretValue under keys.
func credential(name string, keys ...string) *corev1.Secret {
	data := map[string][]byte{}
	for _, k := range keys {
		data[k] = []byte(secretValue)
	}
	s := secret(name, data)
	s.CreationTimestamp = metav1.NewTime(time.Date(2026, 10, 18, 9, 30, 0, 0, time.UTC))
	return s
}

func getSecret(t *testing.T, s *Server, name string) *corev1.Secret {
	t.Helper()
	var out corev1.Secret
	if err := s.Reader.Get(context.Background(), client.ObjectKey{Namespace: tenant, Name: name}, &out); err != nil {
		t.Fatalf("failed to read Secret %q: %v", name, err)
	}
	return &out
}

// credentialFixtures is a Universe namespace with one Secret per kind, the
// Secrets the dashboard hides, and Galaxies and Astrals using them.
func credentialFixtures() []client.Object {
	managed := credential("job-token", shared.SecretKeyToken)
	managed.Labels = map[string]string{corev1alpha1.LabelManagedBy: corev1alpha1.ManagedByValue}
	saToken := credential("sa-token", "token")
	saToken.Type = corev1.SecretTypeServiceAccountToken
	helm := credential("sh.helm.release.v1.x.v1", "release")
	helm.Type = secretTypeHelmRelease

	gal := galaxy("git-app")
	gal.Spec.BackendConfig = &corev1alpha1.BackendConfig{
		Type: corev1alpha1.BackendTypeGCS, Bucket: "b", SecretRef: &corev1alpha1.SecretRef{Name: "gcs"},
	}
	gal.Spec.Discovery = &corev1alpha1.DiscoverySpec{
		Repository: corev1alpha1.VCSRepository{SecretRef: &corev1alpha1.SecretRef{Name: "git-app"}},
	}
	web := astral("web", "https://example.com/r.git", "ssh")
	web.Spec.BackendSecretRef = &corev1alpha1.SecretRef{Name: "gcs"}
	web.Spec.Secrets = []corev1alpha1.EnvSecretRef{{Name: "cloud"}, {Name: "cloud"}}

	return []client.Object{
		readyUniverse(),
		gal, web,
		credential("git-app", shared.SecretKeyGitHubAppID, shared.SecretKeyGitHubAppPrivateKey,
			shared.SecretKeyGitHubAppInstallationID),
		credential("ssh", shared.SecretKeySSHPrivateKey),
		credential("pat", shared.SecretKeyToken),
		credential("basic", shared.SecretKeyUsername, shared.SecretKeyPassword),
		credential("gcs", "GOOGLE_CREDENTIALS"),
		credential("vault", "VAULT_TOKEN", "VAULT_ADDR"),
		credential("cloud", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"),
		managed, saToken, helm,
	}
}

func TestListCredentials(t *testing.T) {
	s := newServer(t, credentialFixtures()...)
	rec := do(t, s, "/api/v1/namespaces/tenant/credentials")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secretValue) {
		t.Fatalf("a Secret value leaked into the response: %s", rec.Body.String())
	}

	got := decode[[]CredentialInfo](t, rec)
	names := make([]string, 0, len(got))
	byName := map[string]CredentialInfo{}
	for _, c := range got {
		names = append(names, c.Name)
		byName[c.Name] = c
	}
	if want := []string{"basic", "cloud", "gcs", "git-app", "pat", "ssh", "vault"}; !slices.Equal(names, want) {
		t.Fatalf("credentials = %v, want %v (sorted, hidden Secrets left out)", names, want)
	}

	kinds := map[string]string{
		"git-app": "GitHubApp", "ssh": "SSHKey", "pat": "Token",
		"basic": "BasicAuth", "gcs": "GCS", "vault": "Vault", "cloud": "Generic",
	}
	for name, kind := range kinds {
		if byName[name].Kind != kind {
			t.Errorf("%s: kind = %q, want %q", name, byName[name].Kind, kind)
		}
	}

	if keys := byName["basic"].Keys; !slices.Equal(keys, []string{"password", "username"}) {
		t.Errorf("basic: keys = %v, want sorted key names", keys)
	}
	if at := byName["basic"].CreatedAt; at != "2026-10-18T09:30:00Z" {
		t.Errorf("basic: createdAt = %q, want RFC3339", at)
	}

	uses := map[string][]CredentialUse{
		"git-app": {{Kind: "Galaxy", Name: "gal", Role: "Discovery"}, {Kind: "Galaxy", Name: "gal", Role: "Git"}},
		"gcs":     {{Kind: "Astral", Name: "web", Role: "State"}, {Kind: "Galaxy", Name: "gal", Role: "State"}},
		"ssh":     {{Kind: "Astral", Name: "web", Role: "Git"}},
		"cloud":   {{Kind: "Astral", Name: "web", Role: "Env"}},
		"pat":     {},
		"basic":   {},
	}
	for name, want := range uses {
		if gotUses := byName[name].UsedBy; !slices.Equal(gotUses, want) {
			t.Errorf("%s: usedBy = %+v, want %+v", name, gotUses, want)
		}
	}
	if !strings.Contains(rec.Body.String(), `"usedBy":[]`) {
		t.Errorf("an unused Secret must report usedBy as [], not null: %s", rec.Body.String())
	}
}

func TestCredentialsOutsideUniverseNamespace(t *testing.T) {
	stray := credential("stray", shared.SecretKeyToken)
	stray.Namespace = "kube-system"
	s := newWriteServer(t, readyUniverse(), universe("young"), stray)

	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/namespaces/kube-system/credentials", ""},
		{http.MethodPost, "/api/v1/namespaces/kube-system/credentials", `{"name":"x","data":{"token":"t"}}`},
		{http.MethodPut, "/api/v1/namespaces/kube-system/credentials/stray", `{"data":{"token":"t"}}`},
		{http.MethodDelete, "/api/v1/namespaces/kube-system/credentials/stray", ""},
		// A Universe whose namespace is not reported yet does not open "".
		{http.MethodGet, "/api/v1/namespaces/stellar-young/credentials", ""},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			got := wantError(t, send(t, s, rt.method, rt.path, rt.body), http.StatusForbidden, "")
			if got.Error != msgCredentialsNamespace {
				t.Errorf("error = %q, want %q", got.Error, msgCredentialsNamespace)
			}
		})
	}
}

func TestCreateCredential(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantField  string
		wantError  string
	}{
		{name: "success", body: `{"name":"new-pat","data":{"token":"` + secretValue + `"}}`,
			wantStatus: http.StatusCreated},
		{name: "duplicate", body: `{"name":"pat","data":{"token":"t"}}`,
			wantStatus: http.StatusConflict, wantField: "name", wantError: msgSecretExists},
		{name: "bad name", body: `{"name":"Not A Name","data":{"token":"t"}}`,
			wantStatus: http.StatusBadRequest, wantField: "name"},
		{name: "no key", body: `{"name":"x","data":{}}`,
			wantStatus: http.StatusBadRequest, wantField: "data", wantError: msgSecretNoKey},
		{name: "bad key", body: `{"name":"x","data":{"bad key":"t"}}`,
			wantStatus: http.StatusBadRequest, wantField: "data",
			wantError: "Invalid key bad key: use letters, digits, '-', '_' or '.'"},
		{name: "empty value", body: `{"name":"x","data":{"token":""}}`,
			wantStatus: http.StatusBadRequest, wantField: "data", wantError: "The value of token is empty"},
		{name: "unknown field", body: `{"name":"x","data":{"token":"t"},"type":"Opaque"}`,
			wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newWriteServer(t, readyUniverse(), credential("pat", shared.SecretKeyToken))
			rec := send(t, s, http.MethodPost, "/api/v1/namespaces/tenant/credentials", tt.body)
			if strings.Contains(rec.Body.String(), secretValue) {
				t.Fatalf("a Secret value leaked into the response: %s", rec.Body.String())
			}
			if tt.wantStatus != http.StatusCreated {
				got := wantError(t, rec, tt.wantStatus, tt.wantField)
				if tt.wantError != "" && got.Error != tt.wantError {
					t.Errorf("error = %q, want %q", got.Error, tt.wantError)
				}
				return
			}
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusCreated, rec.Body.String())
			}
			info := decode[CredentialInfo](t, rec)
			if info.Name != "new-pat" || info.Kind != "Token" || !slices.Equal(info.Keys, []string{"token"}) ||
				info.UsedBy == nil || len(info.UsedBy) != 0 {
				t.Errorf("info = %+v", info)
			}
			stored := getSecret(t, s, "new-pat")
			if string(stored.Data["token"]) != secretValue || stored.Type != corev1.SecretTypeOpaque {
				t.Errorf("stored secret: type=%q, token stored=%v", stored.Type, string(stored.Data["token"]) == secretValue)
			}
			if stored.Labels[labelCreatedBy] != createdByValue || stored.Labels[corev1alpha1.LabelManagedBy] != "" {
				t.Errorf("labels = %v, want created-by only", stored.Labels)
			}
		})
	}
}

func TestUpdateCredential(t *testing.T) {
	stored := credential("pat", shared.SecretKeyToken, "extra")
	stored.Labels = map[string]string{"team": "a"}
	stored.Annotations = map[string]string{"note": "kept"}
	managed := credential("job-token", shared.SecretKeyToken)
	managed.Labels = map[string]string{corev1alpha1.LabelManagedBy: corev1alpha1.ManagedByValue}
	s := newWriteServer(t, readyUniverse(), stored, managed, astral("web", "https://x/r.git", "pat"))

	rec := send(t, s, http.MethodPut, "/api/v1/namespaces/tenant/credentials/pat",
		`{"data":{"username":"bot","password":"`+secretValue+`"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secretValue) {
		t.Fatalf("a Secret value leaked into the response: %s", rec.Body.String())
	}
	info := decode[CredentialInfo](t, rec)
	if info.Kind != "BasicAuth" || !slices.Equal(info.Keys, []string{"password", "username"}) ||
		len(info.UsedBy) != 1 || info.UsedBy[0].Role != "Git" {
		t.Errorf("info = %+v", info)
	}

	got := getSecret(t, s, "pat")
	if _, kept := got.Data["token"]; kept || len(got.Data) != 2 || string(got.Data["username"]) != "bot" {
		t.Errorf("data keys = %v, want exactly the new ones", slices.Sorted(maps.Keys(got.Data)))
	}
	if got.Labels["team"] != "a" || got.Annotations["note"] != "kept" {
		t.Errorf("metadata lost: labels=%v annotations=%v", got.Labels, got.Annotations)
	}

	wantError(t, send(t, s, http.MethodPut, "/api/v1/namespaces/tenant/credentials/pat", `{"data":{}}`),
		http.StatusBadRequest, "data")
	wantError(t, send(t, s, http.MethodPut, "/api/v1/namespaces/tenant/credentials/absent", `{"data":{"a":"b"}}`),
		http.StatusNotFound, "")
	wantError(t, send(t, s, http.MethodPut, "/api/v1/namespaces/tenant/credentials/job-token", `{"data":{"a":"b"}}`),
		http.StatusNotFound, "")
}

func TestDeleteCredential(t *testing.T) {
	s := newWriteServer(t, credentialFixtures()...)

	got := wantError(t, send(t, s, http.MethodDelete, "/api/v1/namespaces/tenant/credentials/gcs", ""),
		http.StatusConflict, "")
	if want := "This Secret is used by: Astral web (State), Galaxy gal (State). Remove it from them first."; got.Error != want {
		t.Errorf("error = %q, want %q", got.Error, want)
	}
	getSecret(t, s, "gcs") // still there

	if rec := send(t, s, http.MethodDelete, "/api/v1/namespaces/tenant/credentials/pat", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	wantError(t, send(t, s, http.MethodDelete, "/api/v1/namespaces/tenant/credentials/pat", ""), http.StatusNotFound, "")

	// Hidden Secrets are not the dashboard's to delete.
	for _, name := range []string{"job-token", "sa-token"} {
		wantError(t, send(t, s, http.MethodDelete, "/api/v1/namespaces/tenant/credentials/"+name, ""),
			http.StatusNotFound, "")
		getSecret(t, s, name)
	}
}
