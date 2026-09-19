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
	"net/http"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
	"github.com/nrx-ops/stellarCD/internal/controller/shared"
)

// +kubebuilder:rbac:groups=core.stellarcd.io,resources=astrals;galaxies,verbs=get;list;watch

const (
	// probeTTL is how long a connectivity verdict is reused. The dashboard
	// polls every few seconds; without this every poll would re-mint a GitHub
	// App token and hit the remote once per repository.
	probeTTL = 60 * time.Second
	// maxConcurrentProbes bounds the fan-out of one request. Checks are network
	// bound, so some parallelism matters, but an operator with a hundred
	// Astrals must not open a hundred sockets at once.
	maxConcurrentProbes = 8
)

// RepositoryInfo is one repository stellarCD is configured to track, together
// with the outcome of a live connectivity check.
//
// It deliberately carries the Secret's *name* and the auth method but never any
// of its contents: the dashboard and the API in front of it are unauthenticated.
type RepositoryInfo struct {
	// Kind is the object the repository was read from, "Astral" or "StellarApp".
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// DisplayName is the human-facing label, when the object carries one.
	DisplayName string `json:"displayName,omitempty"`
	// Universe and Galaxy are empty for a StellarApp, which predates them.
	Universe string `json:"universe,omitempty"`
	Galaxy   string `json:"galaxy,omitempty"`
	Provider string `json:"provider,omitempty"`
	URL      string `json:"url"`
	Branch   string `json:"branch,omitempty"`
	Path     string `json:"path,omitempty"`
	// SecretName is the credentials Secret, and SecretScope says which object
	// supplied it: an Astral may inherit its Galaxy's.
	SecretName  string `json:"secretName,omitempty"`
	SecretScope string `json:"secretScope,omitempty"`
	// SecretMissing records that the referenced Secret does not exist, which is
	// a different failure from bad credentials.
	SecretMissing bool `json:"secretMissing,omitempty"`
	// Connection is the connectivity verdict.
	Connection shared.RepositoryStatus `json:"connection"`
}

// cachedProbe is one memoised verdict.
type cachedProbe struct {
	status shared.RepositoryStatus
	expiry time.Time
}

// probeKey identifies a check. The Secret's resource version is part of it so
// rotating credentials invalidates the verdict immediately instead of leaving a
// stale "Unauthorized" on screen for a minute.
type probeKey struct {
	url             string
	secret          string
	secretNamespace string
	secretVersion   string
}

// cachedCheck returns a memoised verdict, running the probe only when the entry
// is absent or stale.
func (s *Server) cachedCheck(
	ctx context.Context,
	key probeKey,
	creds map[string][]byte,
) shared.RepositoryStatus {
	s.probeMu.Lock()
	if entry, ok := s.probeCache[key]; ok && time.Now().Before(entry.expiry) {
		s.probeMu.Unlock()
		return entry.status
	}
	s.probeMu.Unlock()

	status := shared.CheckRepository(ctx, key.url, creds)

	s.probeMu.Lock()
	if s.probeCache == nil {
		s.probeCache = map[probeKey]cachedProbe{}
	}
	s.probeCache[key] = cachedProbe{status: status, expiry: time.Now().Add(probeTTL)}
	s.probeMu.Unlock()
	return status
}

// repoSource is an object that points at a repository, flattened so Astrals and
// StellarApps can be probed by the same code.
type repoSource struct {
	info       RepositoryInfo
	secretName string
	// secretNamespace is always the object's own namespace: a credentials
	// Secret is never read across a tenant boundary.
	secretNamespace string
}

// handleListRepositories returns every repository stellarCD tracks, each with a
// connectivity status. Scope it with ?namespace=, and skip the live checks with
// ?check=false when only the inventory is wanted.
func (s *Server) handleListRepositories(w http.ResponseWriter, r *http.Request) {
	var opts []client.ListOption
	if ns := r.URL.Query().Get("namespace"); ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}

	sources, err := s.collectRepositories(r.Context(), opts)
	if err != nil {
		s.writeError(w, r, err, "failed to list repositories")
		return
	}

	out := make([]RepositoryInfo, len(sources))
	if r.URL.Query().Get("check") == "false" {
		for i := range sources {
			out[i] = sources[i].info
			out[i].Connection.State = "Unknown"
			out[i].Connection.Message = "connectivity checks were skipped"
		}
	} else {
		s.probeAll(r.Context(), sources, out)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	s.writeJSON(w, r, out)
}

// probeAll fills in the connection status of every source, a bounded number at
// a time. Each entry is independent, so one unreachable host must not delay the
// rest of the page.
func (s *Server) probeAll(ctx context.Context, sources []repoSource, out []RepositoryInfo) {
	var wg sync.WaitGroup
	slots := make(chan struct{}, maxConcurrentProbes)

	for i := range sources {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()

			src := sources[i]
			info := src.info

			creds, version, err := s.readCredentials(ctx, src)
			switch {
			case apierrors.IsNotFound(err):
				info.SecretMissing = true
				info.Connection = shared.RepositoryStatus{
					State:     shared.RepositoryMisconfigured,
					Message:   "the referenced credentials Secret does not exist",
					CheckedAt: time.Now(),
				}
				out[i] = info
				return
			case err != nil:
				s.Log.Error(err, "Failed to read repository credentials",
					"namespace", src.secretNamespace, "secret", src.secretName)
				info.Connection = shared.RepositoryStatus{
					State:     shared.RepositoryMisconfigured,
					Message:   "the credentials Secret could not be read",
					CheckedAt: time.Now(),
				}
				out[i] = info
				return
			}

			info.Connection = s.cachedCheck(ctx, probeKey{
				url:             info.URL,
				secret:          src.secretName,
				secretNamespace: src.secretNamespace,
				secretVersion:   version,
			}, creds)
			out[i] = info
		}(i)
	}
	wg.Wait()
}

// readCredentials loads the Secret backing a repository. It returns a nil map
// when no Secret is referenced, which the probe treats as an anonymous clone.
func (s *Server) readCredentials(ctx context.Context, src repoSource) (map[string][]byte, string, error) {
	if src.secretName == "" {
		return nil, "", nil
	}
	var secret corev1.Secret
	key := client.ObjectKey{Namespace: src.secretNamespace, Name: src.secretName}
	if err := s.Reader.Get(ctx, key, &secret); err != nil {
		return nil, "", err
	}
	return secret.Data, secret.ResourceVersion, nil
}

// collectRepositories flattens Astrals and StellarApps into one inventory. An
// Astral without its own secretRef inherits the Galaxy's, so the Galaxies are
// read once and indexed rather than fetched per Astral.
func (s *Server) collectRepositories(ctx context.Context, opts []client.ListOption) ([]repoSource, error) {
	var galaxies corev1alpha1.GalaxyList
	if err := s.Reader.List(ctx, &galaxies, opts...); err != nil {
		return nil, err
	}
	galaxySecret := map[string]string{}
	for i := range galaxies.Items {
		g := &galaxies.Items[i]
		if g.Spec.VCSSecretRef != nil {
			galaxySecret[g.Namespace+"/"+g.Name] = g.Spec.VCSSecretRef.Name
		}
	}

	var astrals corev1alpha1.AstralList
	if err := s.Reader.List(ctx, &astrals, opts...); err != nil {
		return nil, err
	}

	sources := make([]repoSource, 0, len(astrals.Items))
	for i := range astrals.Items {
		a := &astrals.Items[i]
		repo := a.Spec.VCSRepository

		secretName, scope := "", ""
		if repo.SecretRef != nil {
			secretName, scope = repo.SecretRef.Name, "Astral"
		} else if inherited, ok := galaxySecret[a.Namespace+"/"+a.Spec.GalaxyRef]; ok {
			secretName, scope = inherited, "Galaxy"
		}

		sources = append(sources, repoSource{
			info: RepositoryInfo{
				Kind:        "Astral",
				Namespace:   a.Namespace,
				Name:        a.Name,
				DisplayName: a.Spec.DisplayName,
				Universe:    a.Spec.UniverseRef,
				Galaxy:      a.Spec.GalaxyRef,
				Provider:    string(repo.Provider),
				URL:         repo.URL,
				Branch:      repo.Branch,
				Path:        repo.Path,
				SecretName:  secretName,
				SecretScope: scope,
			},
			secretName:      secretName,
			secretNamespace: a.Namespace,
		})
	}

	// StellarApp predates the Universe/Galaxy/Astral model but still names a
	// repository, so leaving it out would under-report what the operator tracks.
	var apps corev1alpha1.StellarAppList
	if err := s.Reader.List(ctx, &apps, opts...); err != nil {
		return nil, err
	}
	for i := range apps.Items {
		app := &apps.Items[i]
		secretName := ""
		if app.Spec.GitRepository.SecretRef != nil {
			secretName = app.Spec.GitRepository.SecretRef.Name
		}
		scope := ""
		if secretName != "" {
			scope = "StellarApp"
		}
		sources = append(sources, repoSource{
			info: RepositoryInfo{
				Kind:        "StellarApp",
				Namespace:   app.Namespace,
				Name:        app.Name,
				URL:         app.Spec.GitRepository.URL,
				Branch:      app.Spec.GitRepository.Ref,
				Path:        app.Spec.TerraformPath,
				SecretName:  secretName,
				SecretScope: scope,
			},
			secretName:      secretName,
			secretNamespace: app.Namespace,
		})
	}

	return sources, nil
}
