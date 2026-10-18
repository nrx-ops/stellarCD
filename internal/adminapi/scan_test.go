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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nrx-ops/stellarCD/internal/controller/shared"
)

// fakeGitHub serves a repository tree the way the GitHub API does, and records
// whether the request carried credentials.
func fakeGitHub(t *testing.T, files ...string) (*httptest.Server, *bool) {
	t.Helper()
	authed := new(bool)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/repos/acme/infra/git/trees/main" {
			http.NotFound(w, r)
			return
		}
		_, _, *authed = r.BasicAuth()
		entries := make([]string, 0, len(files))
		for _, f := range files {
			entries = append(entries, fmt.Sprintf(`{"path":%q,"type":"blob"}`, f))
		}
		_, _ = fmt.Fprintf(w, `{"truncated":false,"tree":[%s]}`, strings.Join(entries, ","))
	}))
	t.Cleanup(srv.Close)
	return srv, authed
}

func TestScanRepository(t *testing.T) {
	srv, authed := fakeGitHub(t,
		"envs/dev/app/terragrunt.hcl", "envs/prod/app/terragrunt.hcl", "envs/prod/app/main.tf",
		"modules/vpc/main.tf", "root.hcl",
	)
	names := shared.NameModules([]shared.Module{{Path: "envs/dev/app"}, {Path: "envs/prod/app"}}, "envs/")
	existing := astral(names["envs/dev/app"], srv.URL+"/acme/infra.git", "")
	s := newServer(t, readyUniverse(), existing, credential("pat", shared.SecretKeyToken))

	body := fmt.Sprintf(`{"url":%q,"secret":"pat","terragrunt":true,"path":"envs","stripPrefix":"envs/"}`, srv.URL+"/acme/infra.git")
	rec := send(t, s, http.MethodPost, "/api/v1/namespaces/tenant/scan", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if !*authed {
		t.Error("the tree request carried no credentials")
	}
	got := decode[ScanResult](t, rec)
	if len(got.Modules) != 2 {
		t.Fatalf("modules = %+v, want the two Terragrunt units under envs", got.Modules)
	}
	for _, m := range got.Modules {
		if m.Name != names[m.Path] {
			t.Errorf("%s named %q, want %q", m.Path, m.Name, names[m.Path])
		}
		if want := m.Path == "envs/dev/app"; m.Existing != want {
			t.Errorf("%s existing = %v, want %v", m.Path, m.Existing, want)
		}
	}
	if strings.Contains(rec.Body.String(), secretValue) {
		t.Error("the response leaks the credential value")
	}
}

func TestScanRepositoryFailures(t *testing.T) {
	srv, _ := fakeGitHub(t, "main.tf")
	cases := []struct {
		name, namespace, body, field string
		status                       int
	}{
		{name: "namespace of no Universe", namespace: "elsewhere", body: `{"url":"x"}`, status: http.StatusForbidden},
		{name: "no repository", namespace: tenant, body: `{"url":" "}`, field: "url", status: http.StatusBadRequest},
		{name: "unknown field", namespace: tenant, body: `{"url":"x","bogus":1}`, status: http.StatusBadRequest},
		{name: "missing Secret", namespace: tenant, body: fmt.Sprintf(`{"url":%q,"secret":"nope"}`, srv.URL+"/acme/infra.git"), status: http.StatusNotFound},
		{name: "host refuses", namespace: tenant, body: fmt.Sprintf(`{"url":%q,"branch":"gone"}`, srv.URL+"/acme/infra.git"), field: "url", status: http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t, readyUniverse())
			rec := send(t, s, http.MethodPost, "/api/v1/namespaces/"+tc.namespace+"/scan", tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.status, rec.Body.String())
			}
			if tc.field != "" {
				if got := decode[errorBody](t, rec); got.Field != tc.field {
					t.Errorf("field = %q, want %q", got.Field, tc.field)
				}
			}
		})
	}
}
