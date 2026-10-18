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
	"testing"
	"time"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
)

// readyUniverse is Universe "u", whose controller already reported the
// tenant namespace as its own.
func readyUniverse() *corev1alpha1.Universe {
	u := universe("u")
	u.Status.Namespace = tenant
	return u
}

func TestGetGalaxy(t *testing.T) {
	s := newServer(t, galaxy(""))
	rec := do(t, s, "/api/v1/galaxies/tenant/gal")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := decode[corev1alpha1.Galaxy](t, rec); got.Name != testGalaxy || got.Spec.UniverseRef != "u" {
		t.Errorf("galaxy = %s/%s, want gal of Universe u", got.Name, got.Spec.UniverseRef)
	}
	if rec := do(t, s, "/api/v1/galaxies/tenant/absent"); rec.Code != http.StatusNotFound {
		t.Errorf("missing galaxy: status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestCreateGalaxy(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantField  string
		wantError  string
		check      func(t *testing.T, g *corev1alpha1.Galaxy)
	}{
		{
			name: "success takes the namespace of the Universe and strips cluster-owned fields",
			body: `{"metadata":{"name":"web","namespace":"elsewhere","resourceVersion":"42","uid":"abc",
					"finalizers":["x"],"labels":{"team":"a"}},
				"spec":{"universeRef":"u","backendConfig":{"type":"GCS","bucket":"states"},
					"discovery":{"enabled":true,"repository":{"url":"https://example.com/r.git"},"interval":"10m",
						"template":{"driftDetection":{"enabled":true,"interval":"1h"}}}},
				"status":{"phase":"Active"}}`,
			wantStatus: http.StatusCreated,
			check: func(t *testing.T, g *corev1alpha1.Galaxy) {
				if g.Namespace != tenant {
					t.Errorf("namespace = %q, want %q (from the Universe)", g.Namespace, tenant)
				}
				if g.Status.Phase != "" || len(g.Finalizers) != 0 || g.UID == "abc" {
					t.Errorf("cluster-owned fields kept: phase=%q finalizers=%v uid=%q",
						g.Status.Phase, g.Finalizers, g.UID)
				}
				if g.Labels["team"] != "a" {
					t.Errorf("labels = %v, want the user's labels kept", g.Labels)
				}
				if g.Spec.Discovery.Interval.Duration != 10*time.Minute {
					t.Errorf("discovery interval = %v, want 10m", g.Spec.Discovery.Interval)
				}
			},
		},
		{
			name:       "bad name",
			body:       `{"metadata":{"name":"Not_A_Label"},"spec":{"universeRef":"u"}}`,
			wantStatus: http.StatusBadRequest,
			wantField:  "metadata.name",
		},
		{
			name:       "missing universe",
			body:       `{"metadata":{"name":"web"},"spec":{}}`,
			wantStatus: http.StatusBadRequest,
			wantField:  "spec.universeRef",
		},
		{
			name:       "unknown universe",
			body:       `{"metadata":{"name":"web"},"spec":{"universeRef":"nope"}}`,
			wantStatus: http.StatusBadRequest,
			wantField:  "spec.universeRef",
		},
		{
			name:       "universe not ready",
			body:       `{"metadata":{"name":"web"},"spec":{"universeRef":"young"}}`,
			wantStatus: http.StatusConflict,
			wantError:  msgUniverseNotReady,
		},
		{
			name:       "bucket missing",
			body:       `{"metadata":{"name":"web"},"spec":{"universeRef":"u","backendConfig":{"type":"S3"}}}`,
			wantStatus: http.StatusBadRequest,
			wantField:  "spec.backendConfig.bucket",
		},
		{
			name:       "azure container missing",
			body:       `{"metadata":{"name":"web"},"spec":{"universeRef":"u","backendConfig":{"type":"AzureBlob","storageAccount":"acc"}}}`,
			wantStatus: http.StatusBadRequest,
			wantField:  "spec.backendConfig.container",
		},
		{
			name:       "azure storage account missing",
			body:       `{"metadata":{"name":"web"},"spec":{"universeRef":"u","backendConfig":{"type":"AzureBlob","container":"c"}}}`,
			wantStatus: http.StatusBadRequest,
			wantField:  "spec.backendConfig.storageAccount",
		},
		{
			name:       "discovery without repository",
			body:       `{"metadata":{"name":"web"},"spec":{"universeRef":"u","discovery":{"enabled":true,"repository":{}}}}`,
			wantStatus: http.StatusBadRequest,
			wantField:  "spec.discovery.repository.url",
		},
		{
			name:       "bad discovery interval",
			body:       `{"metadata":{"name":"web"},"spec":{"universeRef":"u","discovery":{"interval":"often","repository":{}}}}`,
			wantStatus: http.StatusBadRequest,
			wantField:  "spec.discovery.interval",
			wantError:  msgDurationFormat,
		},
		{
			name: "bad template drift interval",
			body: `{"metadata":{"name":"web"},"spec":{"universeRef":"u","discovery":{"repository":{},
				"template":{"driftDetection":{"interval":"soon"}}}}}`,
			wantStatus: http.StatusBadRequest,
			wantField:  "spec.discovery.template.driftDetection.interval",
			wantError:  msgDurationFormat,
		},
		{
			name:       "duplicate name",
			body:       `{"metadata":{"name":"gal"},"spec":{"universeRef":"u"}}`,
			wantStatus: http.StatusConflict,
			wantField:  "metadata.name",
		},
		{
			name:       "not JSON",
			body:       `{"metadata":`,
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newWriteServer(t, readyUniverse(), universe("young"), galaxy(""))
			rec := send(t, s, http.MethodPost, "/api/v1/galaxies", tt.body)
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
			tt.check(t, ptr(decode[corev1alpha1.Galaxy](t, rec)))
		})
	}
}

func TestUpdateGalaxy(t *testing.T) {
	current := func(t *testing.T, s *Server) *corev1alpha1.Galaxy {
		t.Helper()
		return ptr(decode[corev1alpha1.Galaxy](t, do(t, s, "/api/v1/galaxies/tenant/gal")))
	}

	t.Run("universe change refused", func(t *testing.T) {
		s := newWriteServer(t, galaxy(""))
		g := current(t, s)
		g.Spec.UniverseRef = "other"
		got := wantError(t, send(t, s, http.MethodPut, "/api/v1/galaxies/tenant/gal", mustJSON(t, g)),
			http.StatusBadRequest, "spec.universeRef")
		if got.Error != msgUniverseImmutable {
			t.Errorf("error = %q, want %q", got.Error, msgUniverseImmutable)
		}
	})

	t.Run("missing resourceVersion", func(t *testing.T) {
		s := newWriteServer(t, galaxy(""))
		g := current(t, s)
		g.ResourceVersion = ""
		wantError(t, send(t, s, http.MethodPut, "/api/v1/galaxies/tenant/gal", mustJSON(t, g)),
			http.StatusBadRequest, "metadata.resourceVersion")
	})

	t.Run("stale resourceVersion", func(t *testing.T) {
		s := newWriteServer(t, galaxy(""))
		g := current(t, s)
		g.ResourceVersion = "1"
		g.Spec.Archived = true
		got := wantError(t, send(t, s, http.MethodPut, "/api/v1/galaxies/tenant/gal", mustJSON(t, g)),
			http.StatusConflict, "")
		if got.Error != msgGalaxyConflict {
			t.Errorf("error = %q, want %q", got.Error, msgGalaxyConflict)
		}
	})

	t.Run("invalid backend refused", func(t *testing.T) {
		s := newWriteServer(t, galaxy(""))
		g := current(t, s)
		g.Spec.BackendConfig = &corev1alpha1.BackendConfig{Type: corev1alpha1.BackendTypeGCS}
		wantError(t, send(t, s, http.MethodPut, "/api/v1/galaxies/tenant/gal", mustJSON(t, g)),
			http.StatusBadRequest, "spec.backendConfig.bucket")
	})

	t.Run("archiving keeps the stored metadata", func(t *testing.T) {
		stored := galaxy("")
		stored.Labels = map[string]string{corev1alpha1.LabelUniverse: "u"}
		stored.Finalizers = []string{corev1alpha1.GalaxyFinalizer}
		s := newWriteServer(t, stored)

		g := current(t, s)
		g.Spec.Archived = true
		g.Spec.UniverseRef = "" // empty means unchanged
		g.Labels = nil          // the stored metadata must win
		g.Finalizers = nil      // idem
		rec := send(t, s, http.MethodPut, "/api/v1/galaxies/tenant/gal", mustJSON(t, g))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
		}
		got := current(t, s)
		if !got.Spec.Archived || got.Spec.UniverseRef != "u" {
			t.Errorf("spec = %+v, want archived and still in Universe u", got.Spec)
		}
		if got.Labels[corev1alpha1.LabelUniverse] != "u" || len(got.Finalizers) != 1 {
			t.Errorf("stored metadata lost: labels=%v finalizers=%v", got.Labels, got.Finalizers)
		}
	})
}

func TestDeleteGalaxy(t *testing.T) {
	t.Run("refused while Astrals reference it", func(t *testing.T) {
		s := newWriteServer(t, galaxy(""),
			astralIn(tenant, "a", "u", testGalaxy),
			astralIn(tenant, "b", "u", testGalaxy),
			astralIn(tenant, "c", "u", "other"),
		)
		got := wantError(t, send(t, s, http.MethodDelete, "/api/v1/galaxies/tenant/gal", ""), http.StatusConflict, "")
		if want := "This Galaxy still has 2 Astrals. Move or delete them first."; got.Error != want {
			t.Errorf("error = %q, want %q", got.Error, want)
		}
		if rec := do(t, s, "/api/v1/galaxies/tenant/gal"); rec.Code != http.StatusOK {
			t.Errorf("galaxy after refused delete: status = %d, want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("empty Galaxy deleted", func(t *testing.T) {
		s := newWriteServer(t, galaxy(""), astralIn(tenant, "c", "u", "other"))
		if rec := send(t, s, http.MethodDelete, "/api/v1/galaxies/tenant/gal", ""); rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusNoContent, rec.Body.String())
		}
		if rec := do(t, s, "/api/v1/galaxies/tenant/gal"); rec.Code != http.StatusNotFound {
			t.Errorf("galaxy after delete: status = %d, want %d", rec.Code, http.StatusNotFound)
		}
		wantError(t, send(t, s, http.MethodDelete, "/api/v1/galaxies/tenant/gal", ""), http.StatusNotFound, "")
	})

	if got := galaxyNotEmptyMessage(1); got != "This Galaxy still has 1 Astral. Move or delete them first." {
		t.Errorf("singular message = %q", got)
	}
}
