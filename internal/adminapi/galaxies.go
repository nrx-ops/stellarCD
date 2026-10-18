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
	"fmt"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
)

// Messages of the Galaxy routes.
const (
	msgGalaxyConflict    = "This Galaxy changed since you opened it. Reload and try again."
	msgUniverseImmutable = "The Universe of a Galaxy cannot be changed"
	msgUniverseNotReady  = "This Universe is not ready yet."
	msgDurationFormat    = "Expected duration format: 5m, 1h"
	msgDNSLabel          = "Use at most 63 lowercase letters, digits or '-', " +
		"starting and ending with a letter or digit"
)

// Duration fields of a Galaxy body. metav1.Duration fails the whole document
// on a bad value, so each is probed to name the culprit.
var galaxyDurationFields = [][]string{
	{"spec", "discovery", "interval"},
	{"spec", "discovery", "template", "driftDetection", "interval"},
}

// handleGetGalaxy returns one Galaxy.
func (s *Server) handleGetGalaxy(w http.ResponseWriter, r *http.Request) {
	var galaxy corev1alpha1.Galaxy
	if err := s.Reader.Get(r.Context(), pathKey(r), &galaxy); err != nil {
		s.writeError(w, r, err, "This Galaxy could not be read.")
		return
	}
	s.writeJSON(w, r, galaxy)
}

// handleCreateGalaxy creates a Galaxy in the namespace of its Universe. The
// body's namespace is ignored: a Galaxy always lives where its Universe says.
func (s *Server) handleCreateGalaxy(w http.ResponseWriter, r *http.Request) {
	in, ok := s.decodeGalaxy(w, r)
	if !ok {
		return
	}
	// Only what a user may choose survives: status, resourceVersion, uid,
	// owner references and finalizers belong to the cluster and the controller.
	galaxy := &corev1alpha1.Galaxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:        in.Name,
			Labels:      in.Labels,
			Annotations: in.Annotations,
		},
		Spec: in.Spec,
	}

	if errs := validation.IsDNS1123Label(galaxy.Name); len(errs) > 0 {
		s.fail(w, r, http.StatusBadRequest, "metadata.name", msgDNSLabel)
		return
	}
	if galaxy.Spec.UniverseRef == "" {
		s.fail(w, r, http.StatusBadRequest, "spec.universeRef", "Choose a Universe")
		return
	}
	if field, msg := validateGalaxySpec(&galaxy.Spec); field != "" {
		s.fail(w, r, http.StatusBadRequest, field, msg)
		return
	}

	var universe corev1alpha1.Universe
	if err := s.Reader.Get(r.Context(), client.ObjectKey{Name: galaxy.Spec.UniverseRef}, &universe); err != nil {
		if apierrors.IsNotFound(err) {
			s.fail(w, r, http.StatusBadRequest, "spec.universeRef", "This Universe does not exist")
			return
		}
		s.writeError(w, r, err, "Failed to read the Universe of this Galaxy.")
		return
	}
	if universe.Status.Namespace == "" {
		s.fail(w, r, http.StatusConflict, "", msgUniverseNotReady)
		return
	}
	galaxy.Namespace = universe.Status.Namespace

	if err := s.Client.Create(r.Context(), galaxy); err != nil {
		if apierrors.IsAlreadyExists(err) {
			s.fail(w, r, http.StatusConflict, "metadata.name", "This name is already used in this Universe")
			return
		}
		s.writeError(w, r, err, "Failed to create the Galaxy.")
		return
	}
	s.logWrite("create-galaxy", galaxy.Namespace, galaxy.Name)
	s.writeStatus(w, r, http.StatusCreated, galaxy)
}

// handleUpdateGalaxy replaces the spec of a Galaxy, keeping its stored
// metadata. The body's resourceVersion makes it an optimistic update.
// Archiving a Galaxy is a spec.archived change like any other.
func (s *Server) handleUpdateGalaxy(w http.ResponseWriter, r *http.Request) {
	key := pathKey(r)
	in, ok := s.decodeGalaxy(w, r)
	if !ok {
		return
	}
	if (in.Name != "" && in.Name != key.Name) || (in.Namespace != "" && in.Namespace != key.Namespace) {
		s.fail(w, r, http.StatusBadRequest, "metadata.name", "The name of a Galaxy cannot be changed")
		return
	}
	if in.ResourceVersion == "" {
		s.fail(w, r, http.StatusBadRequest, "metadata.resourceVersion", "Reload this Galaxy before saving it")
		return
	}

	var current corev1alpha1.Galaxy
	if err := s.Reader.Get(r.Context(), key, &current); err != nil {
		s.writeError(w, r, err, "This Galaxy could not be read.")
		return
	}
	// An omitted Universe means "unchanged"; a different one is refused.
	if in.Spec.UniverseRef == "" {
		in.Spec.UniverseRef = current.Spec.UniverseRef
	}
	if in.Spec.UniverseRef != current.Spec.UniverseRef {
		s.fail(w, r, http.StatusBadRequest, "spec.universeRef", msgUniverseImmutable)
		return
	}
	if field, msg := validateGalaxySpec(&in.Spec); field != "" {
		s.fail(w, r, http.StatusBadRequest, field, msg)
		return
	}

	updated := current.DeepCopy()
	updated.Spec = in.Spec
	updated.ResourceVersion = in.ResourceVersion
	if err := s.Client.Update(r.Context(), updated); err != nil {
		if apierrors.IsConflict(err) {
			s.fail(w, r, http.StatusConflict, "", msgGalaxyConflict)
			return
		}
		s.writeError(w, r, err, "Failed to save the Galaxy.")
		return
	}
	s.logWrite("update-galaxy", updated.Namespace, updated.Name)
	s.writeJSON(w, r, updated)
}

// handleDeleteGalaxy deletes a Galaxy that no Astral references any more. The
// Galaxy finalizer would wait for its Astrals anyway, but it never deletes
// them, so refusing here tells the user what to do instead of leaving the
// Galaxy stuck in Deleting.
func (s *Server) handleDeleteGalaxy(w http.ResponseWriter, r *http.Request) {
	key := pathKey(r)
	var galaxy corev1alpha1.Galaxy
	if err := s.Reader.Get(r.Context(), key, &galaxy); err != nil {
		s.writeError(w, r, err, "This Galaxy could not be read.")
		return
	}

	// The live reader has no field index, so the Galaxy filter runs here.
	var astrals corev1alpha1.AstralList
	if err := s.Reader.List(r.Context(), &astrals, client.InNamespace(key.Namespace)); err != nil {
		s.writeError(w, r, err, "Failed to list the Astrals of this Galaxy.")
		return
	}
	count := 0
	for i := range astrals.Items {
		if astrals.Items[i].Spec.GalaxyRef == key.Name {
			count++
		}
	}
	if count > 0 {
		s.fail(w, r, http.StatusConflict, "", galaxyNotEmptyMessage(count))
		return
	}

	if err := s.Client.Delete(r.Context(), &galaxy); err != nil {
		s.writeError(w, r, err, "Failed to delete the Galaxy.")
		return
	}
	s.logWrite("delete-galaxy", key.Namespace, key.Name)
	w.WriteHeader(http.StatusNoContent)
}

// galaxyNotEmptyMessage tells the user why a Galaxy cannot be deleted yet.
func galaxyNotEmptyMessage(count int) string {
	noun := "Astrals"
	if count == 1 {
		noun = kindAstral
	}
	return fmt.Sprintf("This Galaxy still has %d %s. Move or delete them first.", count, noun)
}

// validateGalaxySpec checks what the CRD schema cannot: the fields a backend
// type needs, and a repository for discovery. It returns the offending field
// and a message, or an empty field when the spec is acceptable.
func validateGalaxySpec(spec *corev1alpha1.GalaxySpec) (string, string) {
	if b := spec.BackendConfig; b != nil {
		switch b.Type {
		case corev1alpha1.BackendTypeGCS, corev1alpha1.BackendTypeS3:
			if strings.TrimSpace(b.Bucket) == "" {
				return "spec.backendConfig.bucket", "Enter the bucket holding the state files"
			}
		case corev1alpha1.BackendTypeAzureBlob:
			if strings.TrimSpace(b.Container) == "" {
				return "spec.backendConfig.container", "Enter the container holding the state files"
			}
			if strings.TrimSpace(b.StorageAccount) == "" {
				return "spec.backendConfig.storageAccount", "Enter the storage account of the container"
			}
		}
	}
	if d := spec.Discovery; d != nil && d.Enabled && strings.TrimSpace(d.Repository.URL) == "" {
		return "spec.discovery.repository.url", "Enter the Git repository to discover Astrals from"
	}
	return "", ""
}

// decodeGalaxy decodes a Galaxy body. Unknown fields are accepted: the body is
// a full Kubernetes object, possibly carrying fields this API ignores.
func (s *Server) decodeGalaxy(w http.ResponseWriter, r *http.Request) (*corev1alpha1.Galaxy, bool) {
	data, ok := s.readBody(w, r)
	if !ok {
		return nil, false
	}
	var galaxy corev1alpha1.Galaxy
	if err := json.Unmarshal(data, &galaxy); err != nil {
		for _, path := range galaxyDurationFields {
			if badDuration(data, path...) {
				s.fail(w, r, http.StatusBadRequest, strings.Join(path, "."), msgDurationFormat)
				return nil, false
			}
		}
		s.fail(w, r, http.StatusBadRequest, "", "The request body is not a valid Galaxy.")
		return nil, false
	}
	return &galaxy, true
}
