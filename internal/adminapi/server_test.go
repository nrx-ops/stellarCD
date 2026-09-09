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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(apiextensionsv1.AddToScheme(s))
	utilruntime.Must(corev1alpha1.AddToScheme(s))
	return s
}

func crd(name, group, kind, plural string, versions ...apiextensionsv1.CustomResourceDefinitionVersion) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group:    group,
			Scope:    apiextensionsv1.NamespaceScoped,
			Names:    apiextensionsv1.CustomResourceDefinitionNames{Kind: kind, Plural: plural},
			Versions: versions,
		},
	}
}

func version(name string, served, storage bool) apiextensionsv1.CustomResourceDefinitionVersion {
	return apiextensionsv1.CustomResourceDefinitionVersion{Name: name, Served: served, Storage: storage}
}

func stellarApp(namespace, name string) *corev1alpha1.StellarApp {
	return &corev1alpha1.StellarApp{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: corev1alpha1.StellarAppSpec{
			GitRepository: corev1alpha1.GitRepositorySpec{URL: "https://example.com/repo.git", Ref: "main"},
			TerraformPath: "live/" + name,
		},
	}
}

func event(namespace, name, reason string, at time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name + "-" + reason},
		InvolvedObject: corev1.ObjectReference{
			Kind:      "StellarApp",
			Namespace: namespace,
			Name:      name,
		},
		Type:           corev1.EventTypeWarning,
		Reason:         reason,
		Message:        reason + " happened",
		Count:          1,
		FirstTimestamp: metav1.NewTime(at),
		LastTimestamp:  metav1.NewTime(at),
	}
}

// newServer wires a Server over a fake client. The fake client resolves field
// selectors only through registered indexes, so the Event selectors used by
// handleListEvents have to be declared here the way the API server provides
// them natively.
func newServer(t *testing.T, objs ...client.Object) *Server {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(objs...).
		WithIndex(&corev1.Event{}, "involvedObject.kind", func(o client.Object) []string {
			return []string{o.(*corev1.Event).InvolvedObject.Kind}
		}).
		WithIndex(&corev1.Event{}, "involvedObject.name", func(o client.Object) []string {
			return []string{o.(*corev1.Event).InvolvedObject.Name}
		}).
		Build()
	return &Server{Reader: c, Addr: ":0", Log: logr.Discard()}
}

// do performs a request against the server's route table.
func do(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to decode response %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestHealthz(t *testing.T) {
	if got := do(t, newServer(t), "/healthz").Code; got != http.StatusOK {
		t.Errorf("status = %d, want %d", got, http.StatusOK)
	}
}

func TestHandleListCRDs(t *testing.T) {
	group := corev1alpha1.GroupVersion.Group

	s := newServer(t,
		crd("stellarapps."+group, group, "StellarApp", "stellarapps",
			version("v1alpha1", true, true),
			version("v1alpha2", false, false), // not served: must be hidden
		),
		crd("widgets.example.com", "example.com", "Widget", "widgets", version("v1", true, true)),
	)

	rec := do(t, s, "/api/v1/crds")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	got := decode[[]CRDInfo](t, rec)
	if len(got) != 1 {
		t.Fatalf("len(crds) = %d, want 1 (foreign groups must be filtered out): %+v", len(got), got)
	}

	want := CRDInfo{
		Name:          "stellarapps." + group,
		Group:         group,
		Kind:          "StellarApp",
		Plural:        "stellarapps",
		Scope:         "Namespaced",
		Versions:      []string{"v1alpha1"},
		StoredVersion: "v1alpha1",
	}
	if got[0].Name != want.Name || got[0].Kind != want.Kind || got[0].Plural != want.Plural {
		t.Errorf("identity = %+v, want %+v", got[0], want)
	}
	if got[0].StoredVersion != want.StoredVersion {
		t.Errorf("storedVersion = %q, want %q", got[0].StoredVersion, want.StoredVersion)
	}
	if len(got[0].Versions) != 1 || got[0].Versions[0] != "v1alpha1" {
		t.Errorf("versions = %v, want [v1alpha1] (unserved versions must be omitted)", got[0].Versions)
	}
}

func TestHandleListNamespaces(t *testing.T) {
	s := newServer(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "zeta"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "alpha"}},
	)

	rec := do(t, s, "/api/v1/namespaces")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	got := decode[[]string](t, rec)
	if len(got) != 2 || got[0] != "alpha" || got[1] != "zeta" {
		t.Errorf("namespaces = %v, want [alpha zeta] (sorted)", got)
	}
}

func TestHandleListStellarApps(t *testing.T) {
	s := newServer(t, stellarApp("dev", "api"), stellarApp("prod", "api"))

	tests := []struct {
		name  string
		path  string
		count int
	}{
		{name: "all namespaces", path: "/api/v1/stellarapps", count: 2},
		{name: "scoped to one namespace", path: "/api/v1/stellarapps?namespace=dev", count: 1},
		{name: "unknown namespace", path: "/api/v1/stellarapps?namespace=nope", count: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, s, tt.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			got := decode[corev1alpha1.StellarAppList](t, rec)
			if len(got.Items) != tt.count {
				t.Errorf("len(items) = %d, want %d", len(got.Items), tt.count)
			}
		})
	}
}

func TestHandleGetStellarApp(t *testing.T) {
	s := newServer(t, stellarApp("dev", "api"))

	t.Run("existing app", func(t *testing.T) {
		rec := do(t, s, "/api/v1/stellarapps/dev/api")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		got := decode[corev1alpha1.StellarApp](t, rec)
		if got.Name != "api" || got.Spec.GitRepository.URL == "" {
			t.Errorf("app = %+v, want the dev/api StellarApp with its spec", got)
		}
	})

	t.Run("missing app maps to 404", func(t *testing.T) {
		rec := do(t, s, "/api/v1/stellarapps/dev/absent")
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})
}

func TestHandleListEvents(t *testing.T) {
	base := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	s := newServer(t,
		stellarApp("dev", "api"),
		event("dev", "api", "Older", base),
		event("dev", "api", "Newer", base.Add(time.Minute)),
		event("dev", "other", "Unrelated", base), // different involvedObject.name
	)

	rec := do(t, s, "/api/v1/stellarapps/dev/api/events")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	got := decode[[]EventInfo](t, rec)
	if len(got) != 2 {
		t.Fatalf("len(events) = %d, want 2 (events for other objects must be excluded): %+v", len(got), got)
	}
	if got[0].Reason != "Older" || got[1].Reason != "Newer" {
		t.Errorf("order = [%s %s], want [Older Newer] (oldest first)", got[0].Reason, got[1].Reason)
	}
}

func TestNeedLeaderElection(t *testing.T) {
	// Every replica serves the dashboard, so the API must not be leader-gated.
	if (&Server{}).NeedLeaderElection() {
		t.Error("NeedLeaderElection() = true, want false")
	}
}
