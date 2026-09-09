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

// Package adminapi serves the read-only HTTP API consumed by the stellarCD
// dashboard. It runs as a manager.Runnable so it inherits the manager's
// ServiceAccount credentials, RBAC and shutdown signal, which keeps the browser
// off the Kubernetes API server entirely.
package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
)

const (
	// shutdownTimeout bounds how long in-flight dashboard requests may delay
	// process exit. It stays below the pod's terminationGracePeriodSeconds.
	shutdownTimeout = 5 * time.Second
	// readHeaderTimeout guards against slow-header clients holding connections.
	readHeaderTimeout = 5 * time.Second
	// maxEvents caps the event history returned for a single StellarApp.
	maxEvents = 100
)

// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch

// Server is the dashboard's HTTP backend.
type Server struct {
	// Reader performs live reads against the API server. The dashboard polls
	// cluster-scoped types the manager does not watch (CRDs, Namespaces) and
	// wants fresh data, so caching them would cost more than it saves.
	Reader client.Reader
	// Addr is the listen address, e.g. ":8080".
	Addr string
	Log  logr.Logger
}

// NeedLeaderElection reports that the dashboard API must be served by every
// replica, not only the elected leader.
func (s *Server) NeedLeaderElection() bool {
	return false
}

// Start runs the HTTP server until ctx is cancelled, then drains it. It
// implements manager.Runnable.
func (s *Server) Start(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	// ListenAndServe blocks, so a bind failure has to travel back over a channel
	// rather than being dropped on the floor.
	serveErr := make(chan error, 1)
	go func() {
		s.Log.Info("Starting admin API server", "addr", s.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("admin API server failed: %w", err)
			return
		}
		close(serveErr)
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		s.Log.Info("Shutting down admin API server")
		// ctx is already cancelled, so the drain needs its own deadline.
		drainCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(drainCtx); err != nil {
			return fmt.Errorf("failed to shut down admin API server: %w", err)
		}
		return nil
	}
}

// Handler builds the route table. Exported so tests can exercise it without
// binding a port.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /api/v1/crds", s.handleListCRDs)
	mux.HandleFunc("GET /api/v1/namespaces", s.handleListNamespaces)
	mux.HandleFunc("GET /api/v1/stellarapps", s.handleListStellarApps)
	mux.HandleFunc("GET /api/v1/stellarapps/{namespace}/{name}", s.handleGetStellarApp)
	mux.HandleFunc("GET /api/v1/stellarapps/{namespace}/{name}/events", s.handleListEvents)
	return mux
}

// CRDInfo describes one CustomResourceDefinition owned by stellarCD.
type CRDInfo struct {
	// Name is the CRD object name, i.e. "<plural>.<group>".
	Name string `json:"name"`
	// Group is the API group, e.g. "core.stellarcd.io".
	Group string `json:"group"`
	// Kind is the CamelCase Kind, e.g. "StellarApp".
	Kind string `json:"kind"`
	// Plural is the lowercase resource name used in API paths, e.g. "stellarapps".
	Plural string `json:"plural"`
	// Scope is "Namespaced" or "Cluster".
	Scope string `json:"scope"`
	// Versions lists every served version.
	Versions []string `json:"versions"`
	// StoredVersion is the version the API server persists.
	StoredVersion string `json:"storedVersion"`
}

// handleListCRDs reports the CRDs actually installed in the cluster, restricted
// to stellarCD's own API group. Reading them live means the dashboard cannot
// claim a CRD is deployed when it is not.
func (s *Server) handleListCRDs(w http.ResponseWriter, r *http.Request) {
	var list apiextensionsv1.CustomResourceDefinitionList
	if err := s.Reader.List(r.Context(), &list); err != nil {
		s.writeError(w, r, err, "failed to list custom resource definitions")
		return
	}

	group := corev1alpha1.GroupVersion.Group
	out := make([]CRDInfo, 0, len(list.Items))
	for i := range list.Items {
		crd := &list.Items[i]
		if crd.Spec.Group != group {
			continue
		}

		info := CRDInfo{
			Name:     crd.Name,
			Group:    crd.Spec.Group,
			Kind:     crd.Spec.Names.Kind,
			Plural:   crd.Spec.Names.Plural,
			Scope:    string(crd.Spec.Scope),
			Versions: make([]string, 0, len(crd.Spec.Versions)),
		}
		for _, v := range crd.Spec.Versions {
			if !v.Served {
				continue
			}
			info.Versions = append(info.Versions, v.Name)
			if v.Storage {
				info.StoredVersion = v.Name
			}
		}
		out = append(out, info)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	s.writeJSON(w, r, out)
}

// handleListNamespaces returns the namespace names the operator can see.
func (s *Server) handleListNamespaces(w http.ResponseWriter, r *http.Request) {
	var list corev1.NamespaceList
	if err := s.Reader.List(r.Context(), &list); err != nil {
		s.writeError(w, r, err, "failed to list namespaces")
		return
	}

	out := make([]string, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, list.Items[i].Name)
	}
	sort.Strings(out)
	s.writeJSON(w, r, out)
}

// handleListStellarApps lists StellarApps, optionally scoped by ?namespace=.
func (s *Server) handleListStellarApps(w http.ResponseWriter, r *http.Request) {
	var opts []client.ListOption
	if ns := r.URL.Query().Get("namespace"); ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}

	var list corev1alpha1.StellarAppList
	if err := s.Reader.List(r.Context(), &list, opts...); err != nil {
		s.writeError(w, r, err, "failed to list StellarApps")
		return
	}
	s.writeJSON(w, r, list)
}

// handleGetStellarApp returns a single StellarApp.
func (s *Server) handleGetStellarApp(w http.ResponseWriter, r *http.Request) {
	key := client.ObjectKey{
		Namespace: r.PathValue("namespace"),
		Name:      r.PathValue("name"),
	}

	var app corev1alpha1.StellarApp
	if err := s.Reader.Get(r.Context(), key, &app); err != nil {
		s.writeError(w, r, err, "failed to get StellarApp")
		return
	}
	s.writeJSON(w, r, app)
}

// EventInfo is a trimmed Kubernetes Event for display in the dashboard.
type EventInfo struct {
	Type           string       `json:"type"`
	Reason         string       `json:"reason"`
	Message        string       `json:"message"`
	Count          int32        `json:"count"`
	FirstTimestamp *metav1.Time `json:"firstTimestamp,omitempty"`
	LastTimestamp  *metav1.Time `json:"lastTimestamp,omitempty"`
}

// eventTime picks the most meaningful timestamp on an Event: the legacy
// LastTimestamp when set, otherwise the EventSeries-era EventTime.
func eventTime(e *corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.FirstTimestamp.Time
}

// nilIfZero keeps unset Kubernetes timestamps out of the JSON payload instead of
// emitting a zero date the dashboard would have to special-case.
func nilIfZero(t metav1.Time) *metav1.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// handleListEvents returns the Events recorded against one StellarApp. The
// controller emits its progress and failures as Events, so this is the
// operator-visible history for an app; it is not container log output.
func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	name := r.PathValue("name")

	var list corev1.EventList
	err := s.Reader.List(r.Context(), &list,
		client.InNamespace(namespace),
		client.MatchingFields{
			"involvedObject.kind": "StellarApp",
			"involvedObject.name": name,
		},
	)
	if err != nil {
		s.writeError(w, r, err, "failed to list events")
		return
	}

	sort.Slice(list.Items, func(i, j int) bool {
		return eventTime(&list.Items[i]).Before(eventTime(&list.Items[j]))
	})

	if len(list.Items) > maxEvents {
		list.Items = list.Items[len(list.Items)-maxEvents:]
	}

	out := make([]EventInfo, 0, len(list.Items))
	for i := range list.Items {
		e := &list.Items[i]
		out = append(out, EventInfo{
			Type:           e.Type,
			Reason:         e.Reason,
			Message:        e.Message,
			Count:          e.Count,
			FirstTimestamp: nilIfZero(e.FirstTimestamp),
			LastTimestamp:  nilIfZero(e.LastTimestamp),
		})
	}
	s.writeJSON(w, r, out)
}

func (s *Server) writeJSON(w http.ResponseWriter, r *http.Request, payload any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// The status line is already sent, so log and drop the connection.
		s.Log.Error(err, "Failed to encode admin API response", "path", r.URL.Path)
	}
}

// writeError maps an API error to a status code without echoing internals back
// to the browser; the detail goes to the operator log instead.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error, msg string) {
	status := http.StatusInternalServerError
	switch {
	case apierrors.IsNotFound(err):
		status = http.StatusNotFound
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		status = http.StatusForbidden
	}

	s.Log.Error(err, msg, "path", r.URL.Path, "status", status)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": msg}); err != nil {
		s.Log.Error(err, "Failed to encode admin API error response", "path", r.URL.Path)
	}
}
