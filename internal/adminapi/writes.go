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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
)

const (
	// maxBodyBytes caps a request body. An Astral is a few kilobytes.
	maxBodyBytes = 1 << 20
	// maxGenerateNamePrefix leaves room for the 5 random characters the API
	// server appends, so a generated Flare name fits the 63 characters of the
	// label the controller puts on its Job.
	maxGenerateNamePrefix = 63 - 5
)

// Messages of the write routes.
const (
	msgWritesDisabled = "Writes are disabled on this admin API. " +
		"Start the operator with --admin-api-writes to enable them."
	msgAstralConflict  = "This Astral changed since you opened it. Reload and try again."
	msgGalaxyImmutable = "The Galaxy of an Astral cannot be changed"
	msgFlareFinished   = "This Flare already finished."
)

// AstralUpdate is the answer to a saved Astral: the stored object and the Plan
// Flare the change raised, null when the spec did not change.
type AstralUpdate struct {
	Astral *corev1alpha1.Astral `json:"astral"`
	Flare  *corev1alpha1.Flare  `json:"flare"`
}

// FlareRequest asks for a manual run of an Astral.
type FlareRequest struct {
	Action   corev1alpha1.FlareAction `json:"action"`
	UnitPath string                   `json:"unitPath,omitempty"`
}

// ApproveRequest records a reviewer's decision on a held Flare.
type ApproveRequest struct {
	Approved *bool `json:"approved"`
}

// ConfirmDestroyRequest releases an apply whose plan destroys resources. The
// user types the Astral name, the same safeguard as spec.confirmDestroy.
type ConfirmDestroyRequest struct {
	Confirm string `json:"confirm"`
}

// writes gates a write route behind AllowWrites.
func (s *Server) writes(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.AllowWrites {
			s.fail(w, r, http.StatusForbidden, "", msgWritesDisabled)
			return
		}
		next(w, r)
	}
}

// logWrite records a write without its body, which may name Secrets.
func (s *Server) logWrite(action, namespace, name string) {
	s.Log.Info("Admin API write", "action", action, "namespace", namespace, "name", name)
}

// handleCreateAstral creates an Astral from the dashboard. The controller
// adopts it into its Galaxy (owner reference, labels, finalizer) on its first
// reconcile, exactly as for an Astral applied with kubectl.
func (s *Server) handleCreateAstral(w http.ResponseWriter, r *http.Request) {
	in, ok := s.decodeAstral(w, r)
	if !ok {
		return
	}
	// Only what a user may choose survives: status, resourceVersion, uid,
	// owner references and finalizers belong to the cluster and the controller.
	astral := &corev1alpha1.Astral{
		ObjectMeta: metav1.ObjectMeta{
			Name:        in.Name,
			Namespace:   in.Namespace,
			Labels:      in.Labels,
			Annotations: in.Annotations,
		},
		Spec: in.Spec,
	}

	if astral.Namespace == "" {
		s.fail(w, r, http.StatusBadRequest, "metadata.namespace", "Choose a namespace")
		return
	}
	if errs := validation.IsDNS1123Label(astral.Name); len(errs) > 0 {
		s.fail(w, r, http.StatusBadRequest, "metadata.name",
			"Use at most 63 lowercase letters, digits or '-', starting and ending with a letter or digit")
		return
	}
	if !s.completeFromGalaxy(w, r, astral) {
		return
	}

	if err := s.Client.Create(r.Context(), astral); err != nil {
		if apierrors.IsAlreadyExists(err) {
			s.fail(w, r, http.StatusConflict, "metadata.name", "This name is already used in this Galaxy")
			return
		}
		s.writeError(w, r, err, "Failed to create the Astral.")
		return
	}
	s.logWrite("create-astral", astral.Namespace, astral.Name)
	s.writeStatus(w, r, http.StatusCreated, astral)
}

// handleUpdateAstral replaces the spec of an Astral. The body's
// resourceVersion makes it an optimistic update, and a saved change raises a
// manual Plan so the user sees what the new configuration would do.
func (s *Server) handleUpdateAstral(w http.ResponseWriter, r *http.Request) {
	key := pathKey(r)
	in, ok := s.decodeAstral(w, r)
	if !ok {
		return
	}
	if (in.Name != "" && in.Name != key.Name) || (in.Namespace != "" && in.Namespace != key.Namespace) {
		s.fail(w, r, http.StatusBadRequest, "metadata.name", "The name of an Astral cannot be changed")
		return
	}
	if in.ResourceVersion == "" {
		s.fail(w, r, http.StatusBadRequest, "metadata.resourceVersion", "Reload this Astral before saving it")
		return
	}

	var current corev1alpha1.Astral
	if err := s.Reader.Get(r.Context(), key, &current); err != nil {
		s.writeError(w, r, err, "This Astral could not be read.")
		return
	}
	// An omitted parent means "unchanged"; a different one is refused.
	if in.Spec.GalaxyRef == "" {
		in.Spec.GalaxyRef = current.Spec.GalaxyRef
	}
	if in.Spec.UniverseRef == "" {
		in.Spec.UniverseRef = current.Spec.UniverseRef
	}
	if in.Spec.GalaxyRef != current.Spec.GalaxyRef {
		s.fail(w, r, http.StatusBadRequest, "spec.galaxyRef", msgGalaxyImmutable)
		return
	}
	if in.Spec.UniverseRef != current.Spec.UniverseRef {
		s.fail(w, r, http.StatusBadRequest, "spec.universeRef", msgGalaxyImmutable)
		return
	}

	updated := current.DeepCopy()
	updated.Spec = in.Spec
	updated.ResourceVersion = in.ResourceVersion
	if !s.completeFromGalaxy(w, r, updated) {
		return
	}
	changed := !apiequality.Semantic.DeepEqual(current.Spec, updated.Spec)

	if err := s.Client.Update(r.Context(), updated); err != nil {
		if apierrors.IsConflict(err) {
			s.fail(w, r, http.StatusConflict, "", msgAstralConflict)
			return
		}
		s.writeError(w, r, err, "Failed to save the Astral.")
		return
	}
	s.logWrite("update-astral", updated.Namespace, updated.Name)

	out := AstralUpdate{Astral: updated}
	if changed {
		flare, err := s.createFlare(r.Context(), updated, corev1alpha1.FlareActionPlan, "")
		if err != nil {
			s.writeError(w, r, err, "The Astral was saved, but its plan could not be started.")
			return
		}
		out.Flare = flare
	}
	s.writeJSON(w, r, out)
}

// handleDeleteAstral deletes an Astral. Its finalizer only removes its Flares
// once no Job is running: deleting an Astral never destroys infrastructure.
func (s *Server) handleDeleteAstral(w http.ResponseWriter, r *http.Request) {
	key := pathKey(r)
	astral := &corev1alpha1.Astral{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}}
	if err := s.Client.Delete(r.Context(), astral); err != nil {
		s.writeError(w, r, err, "Failed to delete the Astral.")
		return
	}
	s.logWrite("delete-astral", key.Namespace, key.Name)
	w.WriteHeader(http.StatusNoContent)
}

// handleCreateFlare raises a manual Plan or Apply. A Flare already running
// is not a reason to refuse: the controller makes the new one wait for the
// Astral's run lock.
func (s *Server) handleCreateFlare(w http.ResponseWriter, r *http.Request) {
	var req FlareRequest
	if !s.decodeStrict(w, r, &req) {
		return
	}
	if req.Action != corev1alpha1.FlareActionPlan && req.Action != corev1alpha1.FlareActionApply {
		s.fail(w, r, http.StatusBadRequest, "action", "Choose Plan or Apply")
		return
	}

	var astral corev1alpha1.Astral
	if err := s.Reader.Get(r.Context(), pathKey(r), &astral); err != nil {
		s.writeError(w, r, err, "This Astral could not be read.")
		return
	}
	unitPath := strings.TrimSpace(req.UnitPath)
	if unitPath != "" {
		if !astral.Spec.Terragrunt.RunAll() {
			s.fail(w, r, http.StatusBadRequest, "unitPath", "Only a Terragrunt run-all Astral can target a unit")
			return
		}
		if strings.Contains(unitPath, "..") || strings.HasPrefix(unitPath, "/") {
			s.fail(w, r, http.StatusBadRequest, "unitPath", "The unit path must stay inside the Astral path")
			return
		}
	}
	if !astral.DeletionTimestamp.IsZero() {
		s.fail(w, r, http.StatusConflict, "", "This Astral is being deleted.")
		return
	}

	flare, err := s.createFlare(r.Context(), &astral, req.Action, unitPath)
	if err != nil {
		s.writeError(w, r, err, "Failed to start the Flare.")
		return
	}
	s.writeStatus(w, r, http.StatusCreated, flare)
}

// handleCancelFlare asks the controller to stop a Flare and delete its Job.
func (s *Server) handleCancelFlare(w http.ResponseWriter, r *http.Request) {
	s.patchFlare(w, r, "cancel-flare", func(flare *corev1alpha1.Flare) (int, string, string) {
		if flare.Status.Phase.IsTerminal() {
			return http.StatusConflict, "", msgFlareFinished
		}
		flare.Spec.Cancel = true
		return 0, "", ""
	})
}

// handleApproveFlare records the reviewer's decision on a Flare the Galaxy
// approval policy holds.
func (s *Server) handleApproveFlare(w http.ResponseWriter, r *http.Request) {
	var req ApproveRequest
	if !s.decodeStrict(w, r, &req) {
		return
	}
	if req.Approved == nil {
		s.fail(w, r, http.StatusBadRequest, "approved", "Choose to approve or reject this Flare")
		return
	}
	s.patchFlare(w, r, "approve-flare", func(flare *corev1alpha1.Flare) (int, string, string) {
		switch flare.Status.Phase {
		case "", corev1alpha1.FlarePhasePending, corev1alpha1.FlarePhaseAwaitingApproval:
		default:
			return http.StatusConflict, "", "This Flare is not waiting for approval."
		}
		flare.Spec.Approved = req.Approved
		return 0, "", ""
	})
}

// handleConfirmDestroy releases an apply held because its plan destroys
// resources. The controller then runs a second Job whose allowlist is exactly
// status.pendingDestroy.
func (s *Server) handleConfirmDestroy(w http.ResponseWriter, r *http.Request) {
	var req ConfirmDestroyRequest
	if !s.decodeStrict(w, r, &req) {
		return
	}
	s.patchFlare(w, r, "confirm-destroy", func(flare *corev1alpha1.Flare) (int, string, string) {
		if flare.Status.Phase != corev1alpha1.FlarePhaseAwaitingConfirmation {
			return http.StatusConflict, "", "This Flare is not waiting for a destroy confirmation."
		}
		if req.Confirm != flare.Spec.AstralRef {
			return http.StatusBadRequest, "confirm", "Type the Astral name to confirm"
		}
		flare.Spec.ConfirmDestroy = flare.Spec.AstralRef
		return 0, "", ""
	})
}

// patchFlare loads a Flare, lets mutate check it and change its spec, then
// merge-patches the difference. mutate returns a non-zero status, a field and
// a message to refuse the change.
func (s *Server) patchFlare(
	w http.ResponseWriter,
	r *http.Request,
	action string,
	mutate func(*corev1alpha1.Flare) (int, string, string),
) {
	var flare corev1alpha1.Flare
	if err := s.Reader.Get(r.Context(), pathKey(r), &flare); err != nil {
		s.writeError(w, r, err, "This Flare could not be read.")
		return
	}
	base := client.MergeFrom(flare.DeepCopy())
	if status, field, msg := mutate(&flare); status != 0 {
		s.fail(w, r, status, field, msg)
		return
	}
	if err := s.Client.Patch(r.Context(), &flare, base); err != nil {
		s.writeError(w, r, err, "Failed to update the Flare.")
		return
	}
	s.logWrite(action, flare.Namespace, flare.Name)
	s.writeJSON(w, r, flare)
}

// completeFromGalaxy checks an Astral against its Galaxy and fills what the
// Galaxy provides: the Universe and the default repository. It answers the
// request and returns false when the Astral is refused.
func (s *Server) completeFromGalaxy(w http.ResponseWriter, r *http.Request, astral *corev1alpha1.Astral) bool {
	if astral.Spec.GalaxyRef == "" {
		s.fail(w, r, http.StatusBadRequest, "spec.galaxyRef", "Choose a Galaxy")
		return false
	}
	var galaxy corev1alpha1.Galaxy
	key := client.ObjectKey{Namespace: astral.Namespace, Name: astral.Spec.GalaxyRef}
	if err := s.Reader.Get(r.Context(), key, &galaxy); err != nil {
		if apierrors.IsNotFound(err) {
			s.fail(w, r, http.StatusBadRequest, "spec.galaxyRef",
				fmt.Sprintf("This Galaxy does not exist in namespace %s", astral.Namespace))
			return false
		}
		s.writeError(w, r, err, "Failed to read the Galaxy of this Astral.")
		return false
	}

	switch astral.Spec.UniverseRef {
	case "":
		astral.Spec.UniverseRef = galaxy.Spec.UniverseRef
	case galaxy.Spec.UniverseRef:
	default:
		s.fail(w, r, http.StatusBadRequest, "spec.universeRef",
			fmt.Sprintf("This Galaxy belongs to Universe %s", galaxy.Spec.UniverseRef))
		return false
	}

	repo := &astral.Spec.VCSRepository
	if strings.TrimSpace(repo.URL) == "" {
		if galaxy.Spec.DefaultVCSURL == "" {
			s.fail(w, r, http.StatusBadRequest, "spec.vcsRepository.url", "Enter a Git repository")
			return false
		}
		repo.URL = galaxy.Spec.DefaultVCSURL
		if repo.Provider == "" {
			repo.Provider = galaxy.Spec.DefaultVCSProvider
		}
	}
	return true
}

// createFlare raises a manual Flare against an Astral, built like the ones
// the controller raises: managed labels and a controller reference to the
// Astral, so the Flare goes with it.
func (s *Server) createFlare(
	ctx context.Context,
	astral *corev1alpha1.Astral,
	action corev1alpha1.FlareAction,
	unitPath string,
) (*corev1alpha1.Flare, error) {
	flare := newManualFlare(astral, action, unitPath)
	if err := controllerutil.SetControllerReference(astral, flare, s.Scheme); err != nil {
		return nil, fmt.Errorf("failed to own the Flare by its Astral: %w", err)
	}
	if err := s.Client.Create(ctx, flare); err != nil {
		return nil, fmt.Errorf("failed to create the Flare: %w", err)
	}
	s.logWrite("create-flare-"+strings.ToLower(string(action)), flare.Namespace, flare.Name)
	return flare, nil
}

// newManualFlare builds a manual Flare. Scope is Unit only when a unit is
// targeted, which the Flare CRD allows for manual Flares alone.
func newManualFlare(astral *corev1alpha1.Astral, action corev1alpha1.FlareAction, unitPath string) *corev1alpha1.Flare {
	scope := corev1alpha1.FlareScopeAll
	if unitPath != "" {
		scope = corev1alpha1.FlareScopeUnit
	}
	labels := map[string]string{
		corev1alpha1.LabelManagedBy: corev1alpha1.ManagedByValue,
		corev1alpha1.LabelUniverse:  astral.Spec.UniverseRef,
		corev1alpha1.LabelGalaxy:    astral.Spec.GalaxyRef,
		corev1alpha1.LabelAstral:    astral.Name,
	}
	return &corev1alpha1.Flare{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: flareNamePrefix(astral.Name, action),
			Namespace:    astral.Namespace,
			Labels:       labels,
		},
		Spec: corev1alpha1.FlareSpec{
			UniverseRef: astral.Spec.UniverseRef,
			GalaxyRef:   astral.Spec.GalaxyRef,
			AstralRef:   astral.Name,
			Action:      action,
			Scope:       scope,
			UnitPath:    unitPath,
			Trigger:     corev1alpha1.FlareTriggerManual,
			// TODO(spec): requestedBy must be an email (CRD pattern) and this
			// API is unauthenticated, so there is no user to name. It stays
			// empty until the dashboard has an identity to pass on.
			Parallelism:       corev1alpha1.DefaultFlareParallelism,
			RefreshBeforePlan: true,
		},
	}
}

// flareNamePrefix is the GenerateName of a manual Flare: "<astral>-<action>-",
// the Astral name cut so the generated name stays within 63 characters.
func flareNamePrefix(astral string, action corev1alpha1.FlareAction) string {
	suffix := "-" + strings.ToLower(string(action)) + "-"
	if limit := maxGenerateNamePrefix - len(suffix); len(astral) > limit {
		astral = astral[:limit]
	}
	return astral + suffix
}

// readBody reads a request body up to maxBodyBytes. It answers the request and
// returns false when the body cannot be read.
func (s *Server) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.fail(w, r, http.StatusRequestEntityTooLarge, "", "The request body is larger than 1 MiB.")
			return nil, false
		}
		s.fail(w, r, http.StatusBadRequest, "", "The request body could not be read.")
		return nil, false
	}
	return data, true
}

// decodeAstral decodes an Astral body. Unknown fields are accepted: the body
// is a full Kubernetes object, possibly carrying fields this API ignores.
func (s *Server) decodeAstral(w http.ResponseWriter, r *http.Request) (*corev1alpha1.Astral, bool) {
	data, ok := s.readBody(w, r)
	if !ok {
		return nil, false
	}
	var astral corev1alpha1.Astral
	if err := json.Unmarshal(data, &astral); err != nil {
		if badDriftInterval(data) {
			s.fail(w, r, http.StatusBadRequest, "spec.driftDetection.interval", "Expected duration format: 5m, 1h")
			return nil, false
		}
		s.fail(w, r, http.StatusBadRequest, "", "The request body is not a valid Astral.")
		return nil, false
	}
	return &astral, true
}

// badDriftInterval reports whether the body's drift interval is what broke
// decoding: metav1.Duration fails the whole document on a bad duration.
func badDriftInterval(data []byte) bool {
	var probe struct {
		Spec struct {
			DriftDetection *struct {
				Interval json.RawMessage `json:"interval"`
			} `json:"driftDetection"`
		} `json:"spec"`
	}
	if json.Unmarshal(data, &probe) != nil || probe.Spec.DriftDetection == nil {
		return false
	}
	raw := probe.Spec.DriftDetection.Interval
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var d metav1.Duration
	return json.Unmarshal(raw, &d) != nil
}

// decodeStrict decodes a small action body, refusing unknown fields so a typo
// in a field name cannot silently do nothing.
func (s *Server) decodeStrict(w http.ResponseWriter, r *http.Request, dst any) bool {
	data, ok := s.readBody(w, r)
	if !ok {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil || dec.More() {
		s.fail(w, r, http.StatusBadRequest, "", "The request body is not valid JSON for this action.")
		return false
	}
	return true
}
