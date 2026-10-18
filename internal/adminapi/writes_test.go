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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
)

// newWriteServer is newServer with the write routes enabled.
func newWriteServer(t *testing.T, objs ...client.Object) *Server {
	t.Helper()
	s := newServer(t, objs...)
	s.AllowWrites = true
	return s
}

// send performs a request with a JSON body against the server's route table.
func send(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// wantError checks an error answer: status, and the field when one is expected.
func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, wantField string) errorBody {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	got := decode[errorBody](t, rec)
	if got.Error == "" {
		t.Errorf("error message is empty: %s", rec.Body.String())
	}
	if got.Field != wantField {
		t.Errorf("field = %q, want %q", got.Field, wantField)
	}
	return got
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("failed to encode %T: %v", v, err)
	}
	return string(data)
}

func getFlare(t *testing.T, s *Server, name string) *corev1alpha1.Flare {
	t.Helper()
	var f corev1alpha1.Flare
	if err := s.Reader.Get(context.Background(), client.ObjectKey{Namespace: tenant, Name: name}, &f); err != nil {
		t.Fatalf("failed to read Flare %q: %v", name, err)
	}
	return &f
}

func TestWritesDisabled(t *testing.T) {
	s := newServer(t, astralIn(tenant, "api", "u", testGalaxy))

	routes := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/astrals"},
		{http.MethodPut, "/api/v1/astrals/tenant/api"},
		{http.MethodDelete, "/api/v1/astrals/tenant/api"},
		{http.MethodPost, "/api/v1/astrals/tenant/api/flares"},
		{http.MethodPost, "/api/v1/flares/tenant/f/cancel"},
		{http.MethodPost, "/api/v1/flares/tenant/f/approve"},
		{http.MethodPost, "/api/v1/flares/tenant/f/confirm-destroy"},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			got := wantError(t, send(t, s, rt.method, rt.path, `{}`), http.StatusForbidden, "")
			if got.Error != msgWritesDisabled {
				t.Errorf("error = %q, want %q", got.Error, msgWritesDisabled)
			}
		})
	}

	// Nothing was deleted behind the 403.
	if rec := do(t, s, "/api/v1/astrals/tenant/api"); rec.Code != http.StatusOK {
		t.Errorf("astral after refused delete: status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestCreateAstral(t *testing.T) {
	withDefaultURL := galaxy("")
	withDefaultURL.Spec.DefaultVCSURL = "https://example.com/default.git"
	withDefaultURL.Spec.DefaultVCSProvider = corev1alpha1.VCSProviderGitLab

	tests := []struct {
		name       string
		galaxy     *corev1alpha1.Galaxy
		body       string
		wantStatus int
		wantField  string
		check      func(t *testing.T, a *corev1alpha1.Astral)
	}{
		{
			name:   "success fills the Universe and strips cluster-owned fields",
			galaxy: galaxy(""),
			body: `{"apiVersion":"core.stellarcd.io/v1alpha1","kind":"Astral",
				"metadata":{"name":"web","namespace":"tenant","resourceVersion":"42","uid":"abc",
					"finalizers":["x"],"labels":{"team":"a"}},
				"spec":{"galaxyRef":"gal","vcsRepository":{"url":"https://example.com/r.git"},
					"driftDetection":{"enabled":true,"interval":"30m"},"unknownField":true},
				"status":{"syncStatus":"Synced"}}`,
			wantStatus: http.StatusCreated,
			check: func(t *testing.T, a *corev1alpha1.Astral) {
				if a.Spec.UniverseRef != "u" {
					t.Errorf("universeRef = %q, want u (from the Galaxy)", a.Spec.UniverseRef)
				}
				if a.Status.SyncStatus != "" || len(a.Finalizers) != 0 || a.UID == "abc" {
					t.Errorf("cluster-owned fields kept: status=%q finalizers=%v uid=%q",
						a.Status.SyncStatus, a.Finalizers, a.UID)
				}
				if a.Labels["team"] != "a" {
					t.Errorf("labels = %v, want the user's labels kept", a.Labels)
				}
				if a.Spec.DriftDetection.Interval.Duration != 30*time.Minute {
					t.Errorf("interval = %v, want 30m", a.Spec.DriftDetection.Interval)
				}
			},
		},
		{
			name:       "repository defaults to the Galaxy's",
			galaxy:     withDefaultURL,
			body:       `{"metadata":{"name":"web","namespace":"tenant"},"spec":{"galaxyRef":"gal"}}`,
			wantStatus: http.StatusCreated,
			check: func(t *testing.T, a *corev1alpha1.Astral) {
				if a.Spec.VCSRepository.URL != withDefaultURL.Spec.DefaultVCSURL ||
					a.Spec.VCSRepository.Provider != corev1alpha1.VCSProviderGitLab {
					t.Errorf("repository = %+v, want the Galaxy default", a.Spec.VCSRepository)
				}
			},
		},
		{
			name:       "duplicate name",
			galaxy:     galaxy(""),
			body:       `{"metadata":{"name":"taken","namespace":"tenant"},"spec":{"galaxyRef":"gal","vcsRepository":{"url":"https://x/r.git"}}}`,
			wantStatus: http.StatusConflict,
			wantField:  "metadata.name",
		},
		{
			name:       "bad name",
			galaxy:     galaxy(""),
			body:       `{"metadata":{"name":"Not_A_Label","namespace":"tenant"},"spec":{"galaxyRef":"gal"}}`,
			wantStatus: http.StatusBadRequest,
			wantField:  "metadata.name",
		},
		{
			name:       "missing namespace",
			galaxy:     galaxy(""),
			body:       `{"metadata":{"name":"web"},"spec":{"galaxyRef":"gal"}}`,
			wantStatus: http.StatusBadRequest,
			wantField:  "metadata.namespace",
		},
		{
			name:       "missing galaxy",
			galaxy:     galaxy(""),
			body:       `{"metadata":{"name":"web","namespace":"tenant"},"spec":{"vcsRepository":{"url":"https://x/r.git"}}}`,
			wantStatus: http.StatusBadRequest,
			wantField:  "spec.galaxyRef",
		},
		{
			name:       "unknown galaxy",
			galaxy:     galaxy(""),
			body:       `{"metadata":{"name":"web","namespace":"tenant"},"spec":{"galaxyRef":"nope","vcsRepository":{"url":"https://x/r.git"}}}`,
			wantStatus: http.StatusBadRequest,
			wantField:  "spec.galaxyRef",
		},
		{
			name:       "universe differs from the Galaxy's",
			galaxy:     galaxy(""),
			body:       `{"metadata":{"name":"web","namespace":"tenant"},"spec":{"universeRef":"other","galaxyRef":"gal","vcsRepository":{"url":"https://x/r.git"}}}`,
			wantStatus: http.StatusBadRequest,
			wantField:  "spec.universeRef",
		},
		{
			name:       "no repository and no Galaxy default",
			galaxy:     galaxy(""),
			body:       `{"metadata":{"name":"web","namespace":"tenant"},"spec":{"galaxyRef":"gal"}}`,
			wantStatus: http.StatusBadRequest,
			wantField:  "spec.vcsRepository.url",
		},
		{
			name:       "bad drift interval",
			galaxy:     galaxy(""),
			body:       `{"metadata":{"name":"web","namespace":"tenant"},"spec":{"galaxyRef":"gal","driftDetection":{"enabled":true,"interval":"soon"}}}`,
			wantStatus: http.StatusBadRequest,
			wantField:  "spec.driftDetection.interval",
		},
		{
			name:       "not JSON",
			galaxy:     galaxy(""),
			body:       `{"metadata":`,
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newWriteServer(t, tt.galaxy, astralIn(tenant, "taken", "u", testGalaxy))
			rec := send(t, s, http.MethodPost, "/api/v1/astrals", tt.body)
			if tt.wantStatus != http.StatusCreated {
				wantError(t, rec, tt.wantStatus, tt.wantField)
				return
			}
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusCreated, rec.Body.String())
			}
			tt.check(t, ptr(decode[corev1alpha1.Astral](t, rec)))

			// No Flare is raised on creation: the controller owns that decision.
			var flares corev1alpha1.FlareList
			if err := s.Reader.List(context.Background(), &flares); err != nil {
				t.Fatalf("list flares: %v", err)
			}
			if len(flares.Items) != 0 {
				t.Errorf("creating an Astral raised %d Flare(s), want 0", len(flares.Items))
			}
		})
	}
}

func TestUpdateAstral(t *testing.T) {
	current := func(t *testing.T, s *Server) *corev1alpha1.Astral {
		t.Helper()
		return ptr(decode[corev1alpha1.Astral](t, do(t, s, "/api/v1/astrals/tenant/api")))
	}

	t.Run("galaxy change refused", func(t *testing.T) {
		s := newWriteServer(t, galaxy(""), astralIn(tenant, "api", "u", testGalaxy))
		a := current(t, s)
		a.Spec.GalaxyRef = "elsewhere"
		rec := send(t, s, http.MethodPut, "/api/v1/astrals/tenant/api", mustJSON(t, a))
		got := wantError(t, rec, http.StatusBadRequest, "spec.galaxyRef")
		if got.Error != msgGalaxyImmutable {
			t.Errorf("error = %q, want %q", got.Error, msgGalaxyImmutable)
		}
	})

	t.Run("missing resourceVersion", func(t *testing.T) {
		s := newWriteServer(t, galaxy(""), astralIn(tenant, "api", "u", testGalaxy))
		a := current(t, s)
		a.ResourceVersion = ""
		rec := send(t, s, http.MethodPut, "/api/v1/astrals/tenant/api", mustJSON(t, a))
		wantError(t, rec, http.StatusBadRequest, "metadata.resourceVersion")
	})

	t.Run("stale resourceVersion", func(t *testing.T) {
		s := newWriteServer(t, galaxy(""), astralIn(tenant, "api", "u", testGalaxy))
		a := current(t, s)
		a.ResourceVersion = "1"
		a.Spec.AutoApply = true
		rec := send(t, s, http.MethodPut, "/api/v1/astrals/tenant/api", mustJSON(t, a))
		got := wantError(t, rec, http.StatusConflict, "")
		if got.Error != msgAstralConflict {
			t.Errorf("error = %q, want %q", got.Error, msgAstralConflict)
		}
	})

	t.Run("saved change raises a Plan", func(t *testing.T) {
		stored := astralIn(tenant, "api", "u", testGalaxy)
		stored.Labels = map[string]string{corev1alpha1.LabelGalaxy: testGalaxy}
		stored.Finalizers = []string{corev1alpha1.AstralFinalizer}
		s := newWriteServer(t, galaxy(""), stored)

		a := current(t, s)
		a.Spec.Description = "new description"
		a.Labels = nil     // the stored metadata must win
		a.Finalizers = nil // idem
		rec := send(t, s, http.MethodPut, "/api/v1/astrals/tenant/api", mustJSON(t, a))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
		}
		got := decode[AstralUpdate](t, rec)
		if got.Astral.Spec.Description != "new description" {
			t.Errorf("description = %q, want the new one", got.Astral.Spec.Description)
		}
		if got.Astral.Labels[corev1alpha1.LabelGalaxy] != testGalaxy || len(got.Astral.Finalizers) != 1 {
			t.Errorf("stored metadata lost: labels=%v finalizers=%v", got.Astral.Labels, got.Astral.Finalizers)
		}
		if got.Flare == nil {
			t.Fatal("flare = null, want a Plan Flare")
		}
		f := getFlare(t, s, got.Flare.Name)
		if f.Spec.Action != corev1alpha1.FlareActionPlan || f.Spec.Scope != corev1alpha1.FlareScopeAll ||
			f.Spec.Trigger != corev1alpha1.FlareTriggerManual {
			t.Errorf("flare spec = %+v, want a manual whole-Astral Plan", f.Spec)
		}
	})

	t.Run("unchanged spec raises nothing", func(t *testing.T) {
		s := newWriteServer(t, galaxy(""), astralIn(tenant, "api", "u", testGalaxy))
		rec := send(t, s, http.MethodPut, "/api/v1/astrals/tenant/api", mustJSON(t, current(t, s)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
		}
		if got := decode[AstralUpdate](t, rec); got.Flare != nil {
			t.Errorf("flare = %s, want null for an unchanged spec", got.Flare.Name)
		}
	})

	t.Run("missing astral", func(t *testing.T) {
		s := newWriteServer(t, galaxy(""))
		body := mustJSON(t, astralIn(tenant, "absent", "u", testGalaxy))
		body = strings.Replace(body, `"metadata":{`, `"metadata":{"resourceVersion":"3",`, 1)
		wantError(t, send(t, s, http.MethodPut, "/api/v1/astrals/tenant/absent", body), http.StatusNotFound, "")
	})
}

func TestDeleteAstral(t *testing.T) {
	s := newWriteServer(t, astralIn(tenant, "api", "u", testGalaxy))

	if rec := send(t, s, http.MethodDelete, "/api/v1/astrals/tenant/api", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if rec := do(t, s, "/api/v1/astrals/tenant/api"); rec.Code != http.StatusNotFound {
		t.Errorf("astral after delete: status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	wantError(t, send(t, s, http.MethodDelete, "/api/v1/astrals/tenant/api", ""), http.StatusNotFound, "")
}

func TestCreateFlare(t *testing.T) {
	runAll := astralIn(tenant, "stack", "u", testGalaxy)
	runAll.Spec.Terragrunt = &corev1alpha1.TerragruntSpec{Enabled: true, Mode: corev1alpha1.TerragruntModeRunAll}

	tests := []struct {
		name       string
		astral     string
		body       string
		wantStatus int
		wantField  string
		wantAction corev1alpha1.FlareAction
		wantScope  corev1alpha1.FlareScope
		wantPrefix string
	}{
		{name: "plan", astral: "api", body: `{"action":"Plan"}`, wantStatus: http.StatusCreated,
			wantAction: corev1alpha1.FlareActionPlan, wantScope: corev1alpha1.FlareScopeAll, wantPrefix: "api-plan-"},
		{name: "apply", astral: "api", body: `{"action":"Apply"}`, wantStatus: http.StatusCreated,
			wantAction: corev1alpha1.FlareActionApply, wantScope: corev1alpha1.FlareScopeAll, wantPrefix: "api-apply-"},
		{name: "one unit of a run-all Astral", astral: "stack", body: `{"action":"Plan","unitPath":"vpc"}`,
			wantStatus: http.StatusCreated, wantAction: corev1alpha1.FlareActionPlan,
			wantScope: corev1alpha1.FlareScopeUnit, wantPrefix: "stack-plan-"},
		{name: "unit on a plain Astral", astral: "api", body: `{"action":"Plan","unitPath":"vpc"}`,
			wantStatus: http.StatusBadRequest, wantField: "unitPath"},
		{name: "unit escaping the path", astral: "stack", body: `{"action":"Plan","unitPath":"../x"}`,
			wantStatus: http.StatusBadRequest, wantField: "unitPath"},
		{name: "absolute unit path", astral: "stack", body: `{"action":"Plan","unitPath":"/etc"}`,
			wantStatus: http.StatusBadRequest, wantField: "unitPath"},
		{name: "destroy is not offered", astral: "api", body: `{"action":"Destroy"}`,
			wantStatus: http.StatusBadRequest, wantField: "action"},
		{name: "unknown field", astral: "api", body: `{"action":"Plan","autoApprove":true}`,
			wantStatus: http.StatusBadRequest},
		{name: "unknown astral", astral: "absent", body: `{"action":"Plan"}`, wantStatus: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newWriteServer(t, astralIn(tenant, "api", "u", testGalaxy), runAll.DeepCopy())
			rec := send(t, s, http.MethodPost, "/api/v1/astrals/tenant/"+tt.astral+"/flares", tt.body)
			if tt.wantStatus != http.StatusCreated {
				wantError(t, rec, tt.wantStatus, tt.wantField)
				return
			}
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusCreated, rec.Body.String())
			}
			f := getFlare(t, s, decode[corev1alpha1.Flare](t, rec).Name)
			if f.Spec.Action != tt.wantAction || f.Spec.Scope != tt.wantScope ||
				f.Spec.Trigger != corev1alpha1.FlareTriggerManual || f.Spec.AstralRef != tt.astral ||
				f.Spec.GalaxyRef != testGalaxy || f.Spec.UniverseRef != "u" ||
				f.Spec.Parallelism != corev1alpha1.DefaultFlareParallelism || !f.Spec.RefreshBeforePlan {
				t.Errorf("flare spec = %+v", f.Spec)
			}
			if !strings.HasPrefix(f.Name, tt.wantPrefix) {
				t.Errorf("name = %q, want prefix %q", f.Name, tt.wantPrefix)
			}
			if f.Labels[corev1alpha1.LabelAstral] != tt.astral || f.Labels[corev1alpha1.LabelGalaxy] != testGalaxy ||
				f.Labels[corev1alpha1.LabelUniverse] != "u" ||
				f.Labels[corev1alpha1.LabelManagedBy] != corev1alpha1.ManagedByValue {
				t.Errorf("labels = %v", f.Labels)
			}
			owner := f.OwnerReferences
			if len(owner) != 1 || owner[0].Kind != kindAstral || owner[0].Name != tt.astral ||
				owner[0].Controller == nil || !*owner[0].Controller {
				t.Errorf("owner references = %+v, want the Astral as controller", owner)
			}
		})
	}
}

func TestFlareNamePrefix(t *testing.T) {
	long := strings.Repeat("a", 253)
	for _, action := range []corev1alpha1.FlareAction{corev1alpha1.FlareActionPlan, corev1alpha1.FlareActionApply} {
		got := flareNamePrefix(long, action)
		if len(got)+5 > 63 {
			t.Errorf("prefix %q is %d long, the generated name would exceed 63", got, len(got))
		}
		if !strings.HasSuffix(got, "-"+strings.ToLower(string(action))+"-") {
			t.Errorf("prefix %q lost its action suffix", got)
		}
	}
	if got := flareNamePrefix("api", corev1alpha1.FlareActionPlan); got != "api-plan-" {
		t.Errorf("prefix = %q, want api-plan-", got)
	}
}

func TestCancelFlare(t *testing.T) {
	now := time.Now()
	s := newWriteServer(t,
		flare("running", "api", corev1alpha1.FlarePhaseRunning, now),
		flare("done", "api", corev1alpha1.FlarePhaseSucceeded, now),
	)

	got := wantError(t, send(t, s, http.MethodPost, "/api/v1/flares/tenant/done/cancel", ""), http.StatusConflict, "")
	if got.Error != msgFlareFinished {
		t.Errorf("error = %q, want %q", got.Error, msgFlareFinished)
	}
	if getFlare(t, s, "done").Spec.Cancel {
		t.Error("a finished Flare was patched")
	}

	rec := send(t, s, http.MethodPost, "/api/v1/flares/tenant/running/cancel", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !decode[corev1alpha1.Flare](t, rec).Spec.Cancel || !getFlare(t, s, "running").Spec.Cancel {
		t.Error("spec.cancel = false, want true")
	}

	wantError(t, send(t, s, http.MethodPost, "/api/v1/flares/tenant/absent/cancel", ""), http.StatusNotFound, "")
}

func TestApproveFlare(t *testing.T) {
	now := time.Now()
	s := newWriteServer(t,
		flare("held", "api", corev1alpha1.FlarePhaseAwaitingApproval, now),
		flare("running", "api", corev1alpha1.FlarePhaseRunning, now),
	)

	wantError(t, send(t, s, http.MethodPost, "/api/v1/flares/tenant/held/approve", `{}`),
		http.StatusBadRequest, "approved")
	wantError(t, send(t, s, http.MethodPost, "/api/v1/flares/tenant/running/approve", `{"approved":true}`),
		http.StatusConflict, "")

	rec := send(t, s, http.MethodPost, "/api/v1/flares/tenant/held/approve", `{"approved":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if a := getFlare(t, s, "held").Spec.Approved; a == nil || *a {
		t.Errorf("spec.approved = %v, want false", a)
	}
}

func TestConfirmDestroy(t *testing.T) {
	now := time.Now()
	s := newWriteServer(t,
		flare("destroying", "api", corev1alpha1.FlarePhaseAwaitingConfirmation, now),
		flare("running", "api", corev1alpha1.FlarePhaseRunning, now),
	)

	wantError(t, send(t, s, http.MethodPost, "/api/v1/flares/tenant/destroying/confirm-destroy", `{"confirm":"ap"}`),
		http.StatusBadRequest, "confirm")
	if getFlare(t, s, "destroying").Spec.ConfirmDestroy != "" {
		t.Error("a wrong confirmation was written")
	}
	wantError(t, send(t, s, http.MethodPost, "/api/v1/flares/tenant/running/confirm-destroy", `{"confirm":"api"}`),
		http.StatusConflict, "")
	wantError(t, send(t, s, http.MethodPost, "/api/v1/flares/tenant/destroying/confirm-destroy", `{"name":"api"}`),
		http.StatusBadRequest, "")

	rec := send(t, s, http.MethodPost, "/api/v1/flares/tenant/destroying/confirm-destroy", `{"confirm":"api"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := getFlare(t, s, "destroying").Spec.ConfirmDestroy; got != "api" {
		t.Errorf("spec.confirmDestroy = %q, want api", got)
	}
}

func TestBodyTooLarge(t *testing.T) {
	s := newWriteServer(t)
	body := `{"metadata":{"name":"` + strings.Repeat("a", maxBodyBytes) + `"}}`
	wantError(t, send(t, s, http.MethodPost, "/api/v1/astrals", body), http.StatusRequestEntityTooLarge, "")
}

func TestInvalidCause(t *testing.T) {
	gk := corev1alpha1.GroupVersion.WithKind(kindAstral).GroupKind()

	err := apierrors.NewInvalid(gk, "web", field.ErrorList{
		field.NotSupported(field.NewPath("spec", "engine"), "Pulumi", []string{"Terraform", "OpenTofu"}),
	})
	if f, msg := invalidCause(err, "fallback"); f != "spec.engine" || !strings.Contains(msg, "Terraform") {
		t.Errorf("invalidCause = %q, %q, want spec.engine and the supported values", f, msg)
	}

	if f, msg := invalidCause(apierrors.NewInvalid(gk, "web", nil), "fallback"); f != "" || msg != "fallback" {
		t.Errorf("invalidCause(no causes) = %q, %q, want the fallback", f, msg)
	}
}

func ptr[T any](v T) *T { return &v }
