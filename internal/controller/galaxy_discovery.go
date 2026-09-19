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
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
	"github.com/nrx-ops/stellarCD/internal/controller/shared"
)

// DefaultDiscoveryInterval paces the repository walk when the spec does not
// set one. It is deliberately much longer than the Galaxy reconcile period:
// module layouts change on the timescale of a pull request, not of a resync.
const DefaultDiscoveryInterval = 10 * time.Minute

// Discovery condition reasons.
const (
	reasonDiscoveryDisabled  = "DiscoveryDisabled"
	reasonDiscoverySucceeded = "DiscoverySucceeded"
	reasonDiscoveryFailed    = "DiscoveryFailed"
	reasonDiscoveryTruncated = "DiscoveryTruncated"
	reasonDiscoveryCapped    = "DiscoveryCapped"
)

// discoveryResult summarises one pass, for the condition message and the log.
type discoveryResult struct {
	found     int
	matched   int
	created   int
	updated   int
	pruned    int
	capped    int
	truncated bool
}

// discover walks the configured repository and materialises one Astral per root
// module. It runs inside the normal Galaxy reconcile loop but rate-limits
// itself to spec.discovery.interval, so the reconcile period and the VCS call
// rate stay independent.
func (r *GalaxyReconciler) discover(ctx context.Context, galaxy *corev1alpha1.Galaxy) error {
	logger := log.FromContext(ctx)
	spec := galaxy.Spec.Discovery

	if spec == nil || !spec.Enabled {
		// An existing condition would otherwise linger and claim a stale count
		// after discovery is switched off.
		if spec == nil {
			apimeta.RemoveStatusCondition(&galaxy.Status.Conditions, corev1alpha1.ConditionDiscovered)
			galaxy.Status.DiscoveredAstrals = 0
			return nil
		}
		applyCondition(&galaxy.Status.Conditions, galaxy.Generation,
			corev1alpha1.ConditionDiscovered, metav1.ConditionFalse, reasonDiscoveryDisabled,
			"Discovery is disabled; existing Astrals are left untouched")
		return nil
	}

	interval := spec.DiscoveryInterval(metav1.Duration{Duration: DefaultDiscoveryInterval})
	if !r.discoveryDue(galaxy, interval.Duration) {
		return nil
	}

	creds, err := r.discoveryCredentials(ctx, galaxy)
	if err != nil {
		return err
	}

	tree, err := shared.ListRepositoryTree(ctx, spec.Repository.URL, spec.Repository.Branch, creds)
	if err != nil {
		return fmt.Errorf("failed to list %s: %w", spec.Repository.URL, err)
	}

	engine := string(galaxy.Spec.Terrarium.Engine)
	modules := shared.DetectModules(tree.Files, engine)
	result := discoveryResult{found: len(modules), truncated: tree.Truncated}

	// spec.repository.path scopes the walk to a subtree; it behaves as an
	// implicit include so it does not have to be repeated in Include.
	include := spec.Include
	if root := trimSlash(spec.Repository.Path); root != "" && root != "." {
		include = append([]string{root}, include...)
	}
	modules = shared.FilterModules(modules, include, spec.Exclude)
	result.matched = len(modules)

	if max := int(spec.MaxAstrals); max > 0 && len(modules) > max {
		result.capped = len(modules) - max
		modules = modules[:max]
	}

	names := shared.NameModules(modules, spec.StripPrefix)
	keep := make(map[string]struct{}, len(modules))
	for _, module := range modules {
		name := names[module.Path]
		keep[name] = struct{}{}

		op, err := r.upsertDiscoveredAstral(ctx, galaxy, name, module)
		if err != nil {
			return err
		}
		switch op {
		case controllerutil.OperationResultCreated:
			result.created++
			r.event(galaxy, corev1.EventTypeNormal, reasonDiscoverySucceeded,
				fmt.Sprintf("Discovered Astral %s from %s", name, module.Path))
		case controllerutil.OperationResultUpdated:
			result.updated++
		}
	}

	if spec.Prune {
		pruned, err := r.pruneDiscoveredAstrals(ctx, galaxy, keep)
		if err != nil {
			return err
		}
		result.pruned = pruned
	}

	now := metav1.Now()
	galaxy.Status.LastDiscoveryTime = &now
	galaxy.Status.DiscoveredAstrals = int32(len(keep))

	reason, status := reasonDiscoverySucceeded, metav1.ConditionTrue
	switch {
	case result.truncated:
		// A truncated tree means the listing is incomplete, so every count
		// derived from it is a lower bound. Reporting success here would be a
		// lie an operator could not detect.
		reason, status = reasonDiscoveryTruncated, metav1.ConditionFalse
	case result.capped > 0:
		reason, status = reasonDiscoveryCapped, metav1.ConditionFalse
	}
	applyCondition(&galaxy.Status.Conditions, galaxy.Generation,
		corev1alpha1.ConditionDiscovered, status, reason, result.message(engine))

	logger.Info("🔭 Repository discovery finished",
		"galaxy", galaxy.Name,
		"repository", spec.Repository.URL,
		"found", result.found,
		"matched", result.matched,
		"created", result.created,
		"updated", result.updated,
		"pruned", result.pruned,
		"skippedOverCap", result.capped,
		"truncated", result.truncated,
	)
	return nil
}

// message renders the condition text. Every number the routine dropped on the
// floor is named, so a partial result never reads as a complete one.
func (d discoveryResult) message(engine string) string {
	msg := fmt.Sprintf("Found %d %s modules, %d matched the filters: %d created, %d updated",
		d.found, engine, d.matched, d.created, d.updated)
	if d.pruned > 0 {
		msg += fmt.Sprintf(", %d pruned", d.pruned)
	}
	if d.capped > 0 {
		msg += fmt.Sprintf(". %d more matched but were skipped by maxAstrals; raise it to take them", d.capped)
	}
	if d.truncated {
		msg += ". The host truncated the file listing, so these counts are a lower bound"
	}
	return msg
}

// discoveryDue rate-limits the walk. A spec change forces a pass immediately:
// waiting out the interval after someone edits the filters would look broken.
func (r *GalaxyReconciler) discoveryDue(galaxy *corev1alpha1.Galaxy, interval time.Duration) bool {
	if galaxy.Status.LastDiscoveryTime == nil {
		return true
	}
	if galaxy.Status.ObservedGeneration != galaxy.Generation {
		return true
	}
	return r.now().Sub(galaxy.Status.LastDiscoveryTime.Time) >= interval
}

// now is injectable so the rate limiter is testable without sleeping.
func (r *GalaxyReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// discoveryCredentials resolves the Secret the walk authenticates with,
// preferring the discovery block's own reference over the Galaxy default.
func (r *GalaxyReconciler) discoveryCredentials(
	ctx context.Context,
	galaxy *corev1alpha1.Galaxy,
) (map[string][]byte, error) {
	ref := galaxy.Spec.Discovery.Repository.SecretRef
	if ref == nil {
		ref = galaxy.Spec.VCSSecretRef
	}
	if ref == nil {
		return nil, nil
	}

	var secret corev1.Secret
	key := types.NamespacedName{Namespace: galaxy.Namespace, Name: ref.Name}
	if err := r.Get(ctx, key, &secret); err != nil {
		return nil, fmt.Errorf("failed to read discovery credentials Secret %q: %w", ref.Name, err)
	}
	return secret.Data, nil
}

// upsertDiscoveredAstral creates or refreshes one Astral.
//
// Only the fields discovery actually owns are written: the module path, the
// repository coordinates and the template defaults. Anything an operator edits
// afterwards on a discovered Astral, such as variables or dependsOn, survives
// the next pass untouched.
func (r *GalaxyReconciler) upsertDiscoveredAstral(
	ctx context.Context,
	galaxy *corev1alpha1.Galaxy,
	name string,
	module shared.Module,
) (controllerutil.OperationResult, error) {
	spec := galaxy.Spec.Discovery
	astral := &corev1alpha1.Astral{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: galaxy.Namespace},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, astral, func() error {
		// Refuse to adopt an Astral somebody wrote by hand: silently taking it
		// over would let a filter change delete work the routine never made.
		if astral.CreationTimestamp.IsZero() ||
			astral.Labels[corev1alpha1.LabelDiscoveredBy] == galaxy.Name {
			if astral.Labels == nil {
				astral.Labels = map[string]string{}
			}
			astral.Labels[corev1alpha1.LabelManagedBy] = corev1alpha1.ManagedByValue
			astral.Labels[corev1alpha1.LabelUniverse] = galaxy.Spec.UniverseRef
			astral.Labels[corev1alpha1.LabelGalaxy] = galaxy.Name
			astral.Labels[corev1alpha1.LabelDiscoveredBy] = galaxy.Name
			for k, v := range galaxy.Spec.Labels {
				astral.Labels[k] = v
			}
			if spec.Template != nil {
				for k, v := range spec.Template.Labels {
					astral.Labels[k] = v
				}
			}
		} else {
			return fmt.Errorf(
				"Astral %q already exists and was not created by discovery; "+
					"rename the module or exclude it", name)
		}

		if astral.Annotations == nil {
			astral.Annotations = map[string]string{}
		}
		astral.Annotations[corev1alpha1.AnnotationDiscoveredPath] = module.Path

		astral.Spec.UniverseRef = galaxy.Spec.UniverseRef
		astral.Spec.GalaxyRef = galaxy.Name
		astral.Spec.DisplayName = module.Path
		astral.Spec.Description = fmt.Sprintf(
			"Discovered from %s (%s)", module.Path, module.Marker)

		repo := spec.Repository
		astral.Spec.VCSRepository = corev1alpha1.VCSRepository{
			Provider:  repo.Provider,
			URL:       repo.URL,
			Branch:    repo.Branch,
			Path:      module.Path,
			SecretRef: repo.SecretRef,
		}
		if astral.Spec.WorkspaceName == "" {
			astral.Spec.WorkspaceName = "default"
		}
		astral.Spec.EnableLocking = true

		if spec.Template != nil {
			astral.Spec.AutoApply = spec.Template.AutoApply
			astral.Spec.PlanOnPR = spec.Template.PlanOnPR
			astral.Spec.ApplyOnMerge = spec.Template.ApplyOnMerge
			astral.Spec.DriftDetection = spec.Template.DriftDetection
		}

		return controllerutil.SetControllerReference(galaxy, astral, r.Scheme)
	})
	if err != nil {
		return op, fmt.Errorf("failed to reconcile discovered Astral %q: %w", name, err)
	}
	return op, nil
}

// pruneDiscoveredAstrals deletes Astrals this Galaxy discovered whose module is
// gone. It matches on the discovery label, so a hand-written Astral in the same
// namespace is never a candidate.
func (r *GalaxyReconciler) pruneDiscoveredAstrals(
	ctx context.Context,
	galaxy *corev1alpha1.Galaxy,
	keep map[string]struct{},
) (int, error) {
	var list corev1alpha1.AstralList
	err := r.List(ctx, &list,
		client.InNamespace(galaxy.Namespace),
		client.MatchingLabels{corev1alpha1.LabelDiscoveredBy: galaxy.Name},
	)
	if err != nil {
		return 0, fmt.Errorf("failed to list discovered Astrals: %w", err)
	}

	stale := make([]string, 0)
	for i := range list.Items {
		if _, ok := keep[list.Items[i].Name]; !ok {
			stale = append(stale, list.Items[i].Name)
		}
	}
	sort.Strings(stale)

	pruned := 0
	for _, name := range stale {
		astral := &corev1alpha1.Astral{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: galaxy.Namespace},
		}
		if err := r.Delete(ctx, astral); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return pruned, fmt.Errorf("failed to prune Astral %q: %w", name, err)
		}
		pruned++
		r.event(galaxy, corev1.EventTypeNormal, reasonDiscoverySucceeded,
			fmt.Sprintf("Pruned Astral %s: its module is gone from the repository", name))
	}
	return pruned, nil
}

// trimSlash normalises a directory for comparison against tree paths.
func trimSlash(p string) string {
	for len(p) > 0 && (p[0] == '/' || p[len(p)-1] == '/') {
		p = trimEdges(p)
	}
	return p
}

func trimEdges(p string) string {
	if len(p) > 0 && p[0] == '/' {
		p = p[1:]
	}
	if len(p) > 0 && p[len(p)-1] == '/' {
		p = p[:len(p)-1]
	}
	return p
}
