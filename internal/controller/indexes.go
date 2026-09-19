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

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
)

// Cache index keys. Counting children by listing the whole namespace and
// filtering in Go would work, but an indexed field selector keeps the cost flat
// as a Galaxy grows to hundreds of Astrals and thousands of Flares.
const (
	IndexAstralGalaxyRef = "spec.galaxyRef"
	IndexFlareAstralRef  = "spec.astralRef"
	IndexFlareGalaxyRef  = "spec.flareGalaxyRef"
)

// SetupIndexes registers every field index the controllers depend on. It runs
// once, before any controller is built, because registering the same key twice
// on a manager is an error.
func SetupIndexes(ctx context.Context, mgr manager.Manager) error {
	indexer := mgr.GetFieldIndexer()

	if err := indexer.IndexField(ctx, &corev1alpha1.Astral{}, IndexAstralGalaxyRef, func(obj client.Object) []string {
		astral, ok := obj.(*corev1alpha1.Astral)
		if !ok || astral.Spec.GalaxyRef == "" {
			return nil
		}
		return []string{astral.Spec.GalaxyRef}
	}); err != nil {
		return fmt.Errorf("failed to index Astral by galaxyRef: %w", err)
	}

	if err := indexer.IndexField(ctx, &corev1alpha1.Flare{}, IndexFlareAstralRef, func(obj client.Object) []string {
		flare, ok := obj.(*corev1alpha1.Flare)
		if !ok || flare.Spec.AstralRef == "" {
			return nil
		}
		return []string{flare.Spec.AstralRef}
	}); err != nil {
		return fmt.Errorf("failed to index Flare by astralRef: %w", err)
	}

	if err := indexer.IndexField(ctx, &corev1alpha1.Flare{}, IndexFlareGalaxyRef, func(obj client.Object) []string {
		flare, ok := obj.(*corev1alpha1.Flare)
		if !ok || flare.Spec.GalaxyRef == "" {
			return nil
		}
		return []string{flare.Spec.GalaxyRef}
	}); err != nil {
		return fmt.Errorf("failed to index Flare by galaxyRef: %w", err)
	}

	return nil
}
