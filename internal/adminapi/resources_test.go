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
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
	"github.com/nrx-ops/stellarCD/internal/controller"
	"github.com/nrx-ops/stellarCD/internal/runspec"
)

// fakeLogs is a LogReader serving a fixed log or error.
type fakeLogs struct {
	log      string
	err      error
	gotJob   string
	gotLines int64
}

func (f *fakeLogs) JobLog(_ context.Context, _, job string, tailLines int64) (string, error) {
	f.gotJob, f.gotLines = job, tailLines
	return f.log, f.err
}

func universe(name string) *corev1alpha1.Universe {
	return &corev1alpha1.Universe{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func galaxyIn(namespace, name, universeRef string) *corev1alpha1.Galaxy {
	return &corev1alpha1.Galaxy{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       corev1alpha1.GalaxySpec{UniverseRef: universeRef},
	}
}

func astralIn(namespace, name, universeRef, galaxyRef string) *corev1alpha1.Astral {
	return &corev1alpha1.Astral{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: corev1alpha1.AstralSpec{
			UniverseRef:   universeRef,
			GalaxyRef:     galaxyRef,
			VCSRepository: corev1alpha1.VCSRepository{URL: "https://example.com/repo.git"},
		},
	}
}

func flare(name, astralRef string, phase corev1alpha1.FlarePhase, created time.Time) *corev1alpha1.Flare {
	return &corev1alpha1.Flare{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         tenant,
			Name:              name,
			CreationTimestamp: metav1.NewTime(created),
		},
		Spec: corev1alpha1.FlareSpec{
			UniverseRef: "u",
			GalaxyRef:   testGalaxy,
			AstralRef:   astralRef,
			Action:      corev1alpha1.FlareActionPlan,
		},
		Status: corev1alpha1.FlareStatus{Phase: phase},
	}
}

func TestHandleConfig(t *testing.T) {
	for _, writes := range []bool{false, true} {
		s := newServer(t)
		s.AllowWrites = writes
		got := decode[ConfigInfo](t, do(t, s, "/api/v1/config"))
		if got.WritesEnabled != writes {
			t.Errorf("writesEnabled = %v, want %v", got.WritesEnabled, writes)
		}
	}
}

func TestHandleListUniverses(t *testing.T) {
	s := newServer(t, universe("zeta"), universe("alpha"))
	rec := do(t, s, "/api/v1/universes")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	got := decode[corev1alpha1.UniverseList](t, rec)
	if len(got.Items) != 2 || got.Items[0].Name != "alpha" || got.Items[1].Name != "zeta" {
		t.Errorf("universes = %v, want [alpha zeta]", names(got.Items))
	}
}

func TestHandleListGalaxies(t *testing.T) {
	s := newServer(t,
		galaxyIn("ns-b", "web", "acme"),
		galaxyIn("ns-a", "data", "acme"),
		galaxyIn("ns-c", "other", "globex"),
	)

	tests := []struct {
		name string
		path string
		want []string
	}{
		{name: "every namespace, sorted by name", path: "/api/v1/galaxies", want: []string{"data", "other", "web"}},
		{name: "one universe", path: "/api/v1/galaxies?universe=acme", want: []string{"data", "web"}},
		{name: "unknown universe", path: "/api/v1/galaxies?universe=nope", want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, s, tt.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			got := names(decode[corev1alpha1.GalaxyList](t, rec).Items)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("galaxies = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHandleListAstrals(t *testing.T) {
	s := newServer(t,
		astralIn("ns-b", "api", "acme", "web"),
		astralIn("ns-a", "db", "acme", "data"),
		astralIn("ns-a", "cache", "acme", "data"),
		astralIn("ns-c", "lb", "globex", "edge"),
	)

	tests := []struct {
		name string
		path string
		want []string
	}{
		{name: "all, by namespace then name", path: "/api/v1/astrals",
			want: []string{"ns-a/cache", "ns-a/db", "ns-b/api", "ns-c/lb"}},
		{name: "universe", path: "/api/v1/astrals?universe=acme",
			want: []string{"ns-a/cache", "ns-a/db", "ns-b/api"}},
		{name: "namespace", path: "/api/v1/astrals?namespace=ns-b", want: []string{"ns-b/api"}},
		{name: "galaxy", path: "/api/v1/astrals?galaxy=edge", want: []string{"ns-c/lb"}},
		{name: "combined", path: "/api/v1/astrals?universe=acme&namespace=ns-a&galaxy=data",
			want: []string{"ns-a/cache", "ns-a/db"}},
		{name: "no match", path: "/api/v1/astrals?universe=globex&galaxy=data", want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, s, tt.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			list := decode[corev1alpha1.AstralList](t, rec)
			got := make([]string, 0, len(list.Items))
			for _, a := range list.Items {
				got = append(got, a.Namespace+"/"+a.Name)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("astrals = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHandleGetAstralAndFlare(t *testing.T) {
	now := time.Now()
	s := newServer(t, astralIn(tenant, "api", "u", testGalaxy), flare("api-plan-x", "api", "", now))

	tests := []struct {
		path string
		want int
	}{
		{path: "/api/v1/astrals/tenant/api", want: http.StatusOK},
		{path: "/api/v1/astrals/tenant/absent", want: http.StatusNotFound},
		{path: "/api/v1/flares/tenant/api-plan-x", want: http.StatusOK},
		{path: "/api/v1/flares/tenant/absent", want: http.StatusNotFound},
	}
	for _, tt := range tests {
		if got := do(t, s, tt.path).Code; got != tt.want {
			t.Errorf("GET %s: status = %d, want %d", tt.path, got, tt.want)
		}
	}
}

func TestHandleListAstralFlares(t *testing.T) {
	base := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	s := newServer(t,
		flare("api-old", "api", corev1alpha1.FlarePhaseSucceeded, base),
		flare("api-new", "api", corev1alpha1.FlarePhaseRunning, base.Add(time.Hour)),
		flare("api-b", "api", corev1alpha1.FlarePhasePending, base.Add(time.Minute)),
		flare("api-c", "api", corev1alpha1.FlarePhasePending, base.Add(time.Minute)),
		flare("db-new", "db", corev1alpha1.FlarePhaseRunning, base.Add(2*time.Hour)),
	)

	rec := do(t, s, "/api/v1/astrals/tenant/api/flares")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	got := names(decode[corev1alpha1.FlareList](t, rec).Items)
	want := []string{"api-new", "api-c", "api-b", "api-old"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("flares = %v, want %v (newest first, then name desc, only astralRef=api)", got, want)
	}
}

func TestHandleFlareLogs(t *testing.T) {
	result, err := runspec.ResultLines("nonce", &runspec.Result{Succeeded: true})
	if err != nil {
		t.Fatalf("ResultLines: %v", err)
	}
	rawLog := "Terraform v1.16.6\n" +
		runspec.StepLine("nonce", corev1alpha1.FlareStepPlan) + "\n" +
		"No changes.\n" +
		strings.Join(result, "\n") + "\n"

	withJob := func(f *corev1alpha1.Flare) *corev1alpha1.Flare {
		f.Status.JobName = f.Name + "-1"
		return f
	}
	now := time.Now()

	tests := []struct {
		name          string
		flare         *corev1alpha1.Flare
		logs          *fakeLogs
		wantStatus    int
		wantAvailable bool
		wantLog       string
		wantMessage   string
	}{
		{
			name:          "protocol lines stripped",
			flare:         withJob(flare("done", "api", corev1alpha1.FlarePhaseSucceeded, now)),
			logs:          &fakeLogs{log: rawLog},
			wantStatus:    http.StatusOK,
			wantAvailable: true,
			wantLog:       "Terraform v1.16.6\nNo changes.\n",
		},
		{
			name:        "pod gone",
			flare:       withJob(flare("old", "api", corev1alpha1.FlarePhaseFailed, now)),
			logs:        &fakeLogs{err: controller.ErrNoPod},
			wantStatus:  http.StatusOK,
			wantMessage: msgLogsGone,
		},
		{
			name:        "pod not created yet",
			flare:       withJob(flare("starting", "api", corev1alpha1.FlarePhaseRunning, now)),
			logs:        &fakeLogs{err: controller.ErrNoPod},
			wantStatus:  http.StatusOK,
			wantMessage: msgLogsNotStarted,
		},
		{
			name:        "no Job yet",
			flare:       flare("waiting", "api", corev1alpha1.FlarePhaseAwaitingApproval, now),
			logs:        &fakeLogs{},
			wantStatus:  http.StatusOK,
			wantMessage: msgLogsNotStarted,
		},
		{
			name:       "read failure",
			flare:      withJob(flare("broken", "api", corev1alpha1.FlarePhaseRunning, now)),
			logs:       &fakeLogs{err: errors.New("connection refused")},
			wantStatus: http.StatusInternalServerError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServer(t, tt.flare)
			s.Logs = tt.logs
			rec := do(t, s, "/api/v1/flares/tenant/"+tt.flare.Name+"/logs")
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus != http.StatusOK {
				if strings.Contains(rec.Body.String(), "connection refused") {
					t.Errorf("internal error leaked to the client: %s", rec.Body.String())
				}
				return
			}
			got := decode[FlareLog](t, rec)
			if got.Available != tt.wantAvailable || got.Log != tt.wantLog || got.Message != tt.wantMessage {
				t.Errorf("log = %+v, want available=%v log=%q message=%q",
					got, tt.wantAvailable, tt.wantLog, tt.wantMessage)
			}
			if tt.flare.Status.JobName != "" && tt.logs.gotLines != logTailLines {
				t.Errorf("tail lines = %d, want %d", tt.logs.gotLines, logTailLines)
			}
		})
	}
}

func TestHandleListSecretNames(t *testing.T) {
	s := newServer(t,
		secret("zeta-git", map[string][]byte{"token": []byte("s3cr3t-value")}),
		secret("aws-creds", map[string][]byte{"AWS_SECRET_ACCESS_KEY": []byte("hidden")}),
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: tenant, Name: "default-token"},
			Type:       corev1.SecretTypeServiceAccountToken,
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: tenant, Name: "sh.helm.release.v1.x.v1"},
			Type:       "helm.sh/release.v1",
		},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "elsewhere", Name: "foreign"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Namespace: tenant,
			Name:      "network-plan-x-1-git",
			Labels:    map[string]string{corev1alpha1.LabelManagedBy: corev1alpha1.ManagedByValue},
		}},
	)

	rec := do(t, s, "/api/v1/namespaces/tenant/secrets")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, leak := range []string{"s3cr3t-value", "hidden", "token", "AWS_SECRET_ACCESS_KEY"} {
		if strings.Contains(body, leak) {
			t.Errorf("response %s leaks %q", body, leak)
		}
	}
	got := decode[[]string](t, rec)
	if strings.Join(got, ",") != "aws-creds,zeta-git" {
		t.Errorf("secrets = %v, want [aws-creds zeta-git]", got)
	}
}

// names returns the object names of a list, in order.
func names[T any, PT interface {
	*T
	GetName() string
}](items []T) []string {
	out := make([]string, 0, len(items))
	for i := range items {
		out = append(out, PT(&items[i]).GetName())
	}
	return out
}
