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
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
	"github.com/nrx-ops/stellarCD/internal/controller"
	"github.com/nrx-ops/stellarCD/internal/runspec"
)

// logTailLines is how much of a runner log the dashboard shows.
const logTailLines = 5000

// Messages shown when a Flare's log cannot be read.
const (
	msgLogsGone       = "Logs for this Flare are no longer available."
	msgLogsNotStarted = "The Job has not started yet."
)

// Secret types that are never credentials a user would pick for an Astral.
var hiddenSecretTypes = []corev1.SecretType{
	corev1.SecretTypeServiceAccountToken,
	"helm.sh/release.v1",
}

// LogReader reads the tail of a Job's runner log. It returns
// controller.ErrNoPod when the Job has no pod, either not created yet or
// already gone. *controller.ClientsetLogReader satisfies it.
type LogReader interface {
	JobLog(ctx context.Context, namespace, job string, tailLines int64) (string, error)
}

// ConfigInfo tells the dashboard what this API allows.
type ConfigInfo struct {
	WritesEnabled bool `json:"writesEnabled"`
}

// FlareLog is the runner output of a Flare, stripped of the protocol lines.
type FlareLog struct {
	// Available is false when there is no log to show; Message says why.
	Available bool   `json:"available"`
	Log       string `json:"log"`
	Message   string `json:"message"`
}

// handleConfig reports whether the write routes are enabled.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, r, ConfigInfo{WritesEnabled: s.AllowWrites})
}

// handleListUniverses lists every Universe, sorted by name.
func (s *Server) handleListUniverses(w http.ResponseWriter, r *http.Request) {
	var list corev1alpha1.UniverseList
	if err := s.Reader.List(r.Context(), &list); err != nil {
		s.writeError(w, r, err, "Failed to list Universes.")
		return
	}
	slices.SortFunc(list.Items, func(a, b corev1alpha1.Universe) int { return cmp.Compare(a.Name, b.Name) })
	s.writeJSON(w, r, list)
}

// handleListGalaxies lists the Galaxies of every namespace, optionally only
// those of ?universe=, sorted by name.
func (s *Server) handleListGalaxies(w http.ResponseWriter, r *http.Request) {
	var list corev1alpha1.GalaxyList
	if err := s.Reader.List(r.Context(), &list); err != nil {
		s.writeError(w, r, err, "Failed to list Galaxies.")
		return
	}
	if universe := r.URL.Query().Get("universe"); universe != "" {
		list.Items = slices.DeleteFunc(list.Items, func(g corev1alpha1.Galaxy) bool {
			return g.Spec.UniverseRef != universe
		})
	}
	slices.SortFunc(list.Items, func(a, b corev1alpha1.Galaxy) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Namespace, b.Namespace))
	})
	s.writeJSON(w, r, list)
}

// handleListAstrals lists Astrals, filtered by the optional ?universe=,
// ?namespace= and ?galaxy= parameters, sorted by namespace then name.
func (s *Server) handleListAstrals(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	var opts []client.ListOption
	if ns := query.Get("namespace"); ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}

	var list corev1alpha1.AstralList
	if err := s.Reader.List(r.Context(), &list, opts...); err != nil {
		s.writeError(w, r, err, "Failed to list Astrals.")
		return
	}
	universe, galaxy := query.Get("universe"), query.Get("galaxy")
	list.Items = slices.DeleteFunc(list.Items, func(a corev1alpha1.Astral) bool {
		return (universe != "" && a.Spec.UniverseRef != universe) ||
			(galaxy != "" && a.Spec.GalaxyRef != galaxy)
	})
	slices.SortFunc(list.Items, func(a, b corev1alpha1.Astral) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	s.writeJSON(w, r, list)
}

// handleGetAstral returns one Astral.
func (s *Server) handleGetAstral(w http.ResponseWriter, r *http.Request) {
	var astral corev1alpha1.Astral
	if err := s.Reader.Get(r.Context(), pathKey(r), &astral); err != nil {
		s.writeError(w, r, err, "This Astral could not be read.")
		return
	}
	s.writeJSON(w, r, astral)
}

// handleListAstralFlares lists the Flares of one Astral, newest first. The
// live reader has no field index, so the Astral filter runs here.
func (s *Server) handleListAstralFlares(w http.ResponseWriter, r *http.Request) {
	key := pathKey(r)
	var list corev1alpha1.FlareList
	if err := s.Reader.List(r.Context(), &list, client.InNamespace(key.Namespace)); err != nil {
		s.writeError(w, r, err, "Failed to list the Flares of this Astral.")
		return
	}
	list.Items = slices.DeleteFunc(list.Items, func(f corev1alpha1.Flare) bool {
		return f.Spec.AstralRef != key.Name
	})
	slices.SortFunc(list.Items, func(a, b corev1alpha1.Flare) int {
		return cmp.Or(
			b.CreationTimestamp.Compare(a.CreationTimestamp.Time),
			cmp.Compare(b.Name, a.Name),
		)
	})
	s.writeJSON(w, r, list)
}

// handleGetFlare returns one Flare.
func (s *Server) handleGetFlare(w http.ResponseWriter, r *http.Request) {
	var flare corev1alpha1.Flare
	if err := s.Reader.Get(r.Context(), pathKey(r), &flare); err != nil {
		s.writeError(w, r, err, "This Flare could not be read.")
		return
	}
	s.writeJSON(w, r, flare)
}

// handleFlareLogs returns the tail of the runner log of a Flare's Job. The
// protocol lines the controller parses are removed: they are noise to a
// reader, and the result they carry is already in the Flare status.
func (s *Server) handleFlareLogs(w http.ResponseWriter, r *http.Request) {
	var flare corev1alpha1.Flare
	if err := s.Reader.Get(r.Context(), pathKey(r), &flare); err != nil {
		s.writeError(w, r, err, "This Flare could not be read.")
		return
	}

	unavailable := FlareLog{Message: msgLogsGone}
	if !flare.Status.Phase.IsTerminal() {
		unavailable.Message = msgLogsNotStarted
	}
	if flare.Status.JobName == "" || s.Logs == nil {
		s.writeJSON(w, r, unavailable)
		return
	}

	log, err := s.Logs.JobLog(r.Context(), flare.Namespace, flare.Status.JobName, logTailLines)
	switch {
	case errors.Is(err, controller.ErrNoPod):
		s.writeJSON(w, r, unavailable)
		return
	case apierrors.IsBadRequest(err) && !flare.Status.Phase.IsTerminal():
		// The pod exists but its container is still being created.
		s.writeJSON(w, r, FlareLog{Message: msgLogsNotStarted})
		return
	case err != nil:
		s.writeError(w, r, err, "Failed to read the logs of this Flare.")
		return
	}
	s.writeJSON(w, r, FlareLog{Available: true, Log: runspec.StripMarkers(log)})
}

// handleListSecretNames returns the names of the Secrets of a namespace, so
// the dashboard can offer them as credentials. Only names leave this handler:
// never data, keys, labels or annotations.
func (s *Server) handleListSecretNames(w http.ResponseWriter, r *http.Request) {
	var list corev1.SecretList
	if err := s.Reader.List(r.Context(), &list, client.InNamespace(r.PathValue("namespace"))); err != nil {
		s.writeError(w, r, err, "Failed to list the Secrets of this namespace.")
		return
	}
	names := make([]string, 0, len(list.Items))
	for i := range list.Items {
		if slices.Contains(hiddenSecretTypes, list.Items[i].Type) {
			continue
		}
		// The controller mints per-Job Secrets (a GitHub App installation
		// token, for instance). They expire with their Job: never offer them.
		if list.Items[i].Labels[corev1alpha1.LabelManagedBy] == corev1alpha1.ManagedByValue {
			continue
		}
		names = append(names, list.Items[i].Name)
	}
	slices.Sort(names)
	s.writeJSON(w, r, names)
}

// pathKey reads the {namespace}/{name} path parameters.
func pathKey(r *http.Request) client.ObjectKey {
	return client.ObjectKey{Namespace: r.PathValue("namespace"), Name: r.PathValue("name")}
}
