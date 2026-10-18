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

package controller

import (
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
	"github.com/nrx-ops/stellarCD/internal/runspec"
)

// TestBuildJobBackendSecret checks RG-Héritage for the state backend
// credentials: the Astral's Secret, else the Galaxy's, injected with the
// Secret's own key names.
func TestBuildJobBackendSecret(t *testing.T) {
	gcs := func(secret string) *corev1alpha1.BackendConfig {
		b := &corev1alpha1.BackendConfig{Type: corev1alpha1.BackendTypeGCS, Bucket: "state"}
		if secret != "" {
			b.SecretRef = &corev1alpha1.SecretRef{Name: secret}
		}
		return b
	}
	cases := []struct {
		name    string
		astral  *corev1alpha1.SecretRef
		backend *corev1alpha1.BackendConfig
		want    []string
	}{
		{name: "none", want: nil},
		{name: "inherited from the Galaxy", backend: gcs("galaxy-gcs"), want: []string{"galaxy-gcs"}},
		{name: "set on the Astral", astral: &corev1alpha1.SecretRef{Name: "astral-gcs"}, want: []string{"astral-gcs"}},
		{
			name:    "the Astral overrides the Galaxy",
			astral:  &corev1alpha1.SecretRef{Name: "astral-gcs"},
			backend: gcs("galaxy-gcs"),
			want:    []string{"astral-gcs"},
		},
		{name: "a Galaxy backend without Secret", backend: gcs(""), want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			astral := &corev1alpha1.Astral{
				ObjectMeta: metav1.ObjectMeta{Name: "network", Namespace: "tenant"},
				Spec: corev1alpha1.AstralSpec{
					BackendSecretRef: tc.astral,
					Secrets:          []corev1alpha1.EnvSecretRef{{Name: "cloud"}},
				},
			}
			galaxy := &corev1alpha1.Galaxy{Spec: corev1alpha1.GalaxySpec{BackendConfig: tc.backend}}
			flare := &corev1alpha1.Flare{
				ObjectMeta: metav1.ObjectMeta{Name: "network-plan-1", Namespace: "tenant"},
				Spec:       corev1alpha1.FlareSpec{AstralRef: "network", Action: corev1alpha1.FlareActionPlan},
			}

			job, err := buildJob(jobInputs{
				flare: flare, astral: astral, galaxy: galaxy, request: &runspec.Request{}, nonce: "n",
			}, RunnerConfig{Image: "runner:test"})
			if err != nil {
				t.Fatalf("buildJob: %v", err)
			}

			var backend []string
			for _, src := range job.Spec.Template.Spec.Containers[0].EnvFrom {
				if src.SecretRef == nil || src.SecretRef.Name == "cloud" {
					continue
				}
				if src.Prefix != "" {
					t.Errorf("backend Secret %s got prefix %q, want its own key names", src.SecretRef.Name, src.Prefix)
				}
				backend = append(backend, src.SecretRef.Name)
			}
			if !slices.Equal(backend, tc.want) {
				t.Errorf("backend Secrets = %v, want %v", backend, tc.want)
			}
		})
	}
}
