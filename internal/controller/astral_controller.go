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
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
)

// DefaultAstralRequeue paces the periodic re-sync of an Astral.
const DefaultAstralRequeue = 2 * time.Minute

// DefaultDriftInterval is used when drift detection is on but no interval is set.
const DefaultDriftInterval = time.Hour

// AnnotationSourceFlare links a chained Flare back to the Flare that triggered
// it. It is what makes auto-apply idempotent: the Astral refuses to raise a
// second Apply for a plan it has already chained.
const AnnotationSourceFlare = "core.stellarcd.io/source-flare"

// AnnotationTrigger records why a Flare was created.
const AnnotationTrigger = "core.stellarcd.io/trigger"

// Trigger values written to AnnotationTrigger.
const (
	triggerDriftDetection = "drift-detection"
	triggerAutoApply      = "auto-apply"
)

// maxNameLength bounds a generated Flare name, leaving room for the suffix.
const maxNameLength = 200

// Astral-specific condition reasons.
const (
	reasonConfigValid        = "ConfigValid"
	reasonConfigInvalid      = "ConfigInvalid"
	reasonDependenciesReady  = "DependenciesReady"
	reasonDependenciesUnmet  = "DependenciesUnmet"
	reasonDriftFree          = "DriftFree"
	reasonDriftDetected      = "DriftDetected"
	reasonDriftUnknown       = "DriftNotChecked"
	reasonFlareRunning       = "FlareRunning"
	reasonFlareChainCreated  = "AutoApplyChained"
	reasonDriftFlareCreated  = "DriftCheckScheduled"
	reasonLastFlareSucceeded = "LastFlareSucceeded"
	reasonLastFlareFailed    = "LastFlareFailed"
)

// AstralReconciler reconciles an Astral: the project level that validates its
// own configuration, waits on its dependencies, watches its Flares and schedules
// the drift checks that keep the plane honest.
type AstralReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// RequeueInterval paces periodic re-sync; zero uses DefaultAstralRequeue.
	RequeueInterval time.Duration
	// Now is injectable so drift scheduling is testable without sleeping.
	Now func() time.Time
}

// +kubebuilder:rbac:groups=core.stellarcd.io,resources=astrals,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core.stellarcd.io,resources=astrals/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.stellarcd.io,resources=astrals/finalizers,verbs=update
// +kubebuilder:rbac:groups=core.stellarcd.io,resources=flares,verbs=get;list;watch;create;update;patch;delete

// Reconcile validates an Astral, resolves its dependencies, folds in the state
// of its Flares and schedules drift detection.
func (r *AstralReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var astral corev1alpha1.Astral
	if err := r.Get(ctx, req.NamespacedName, &astral); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !astral.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &astral)
	}

	galaxy, err := r.resolveGalaxy(ctx, &astral)
	if err != nil {
		return r.fail(ctx, &astral, corev1alpha1.ConditionParentResolved, reasonParentMissing, err)
	}
	applyCondition(&astral.Status.Conditions, astral.Generation,
		corev1alpha1.ConditionParentResolved, metav1.ConditionTrue, reasonParentResolved,
		"Galaxy "+galaxy.Name+" resolved")

	if err := r.adopt(ctx, &astral, galaxy); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.validate(ctx, &astral, galaxy); err != nil {
		return r.fail(ctx, &astral, corev1alpha1.ConditionConfigValid, reasonConfigInvalid, err)
	}
	applyCondition(&astral.Status.Conditions, astral.Generation,
		corev1alpha1.ConditionConfigValid, metav1.ConditionTrue, reasonConfigValid,
		"Repository, engine and secret references are coherent")

	flares, err := r.listFlares(ctx, &astral)
	if err != nil {
		return r.fail(ctx, &astral, corev1alpha1.ConditionReconciled, reasonReconcileFailed, err)
	}
	active := r.observeFlares(&astral, flares)

	pending, err := r.dependenciesReady(ctx, &astral)
	if err != nil {
		return r.fail(ctx, &astral, corev1alpha1.ConditionDependenciesReady, reasonReconcileFailed, err)
	}
	if len(pending) > 0 {
		astral.Status.Phase = corev1alpha1.AstralPhasePending
		applyCondition(&astral.Status.Conditions, astral.Generation,
			corev1alpha1.ConditionDependenciesReady, metav1.ConditionFalse, reasonDependenciesUnmet,
			"Waiting for Astrals: "+strings.Join(pending, ", "))
	} else {
		applyCondition(&astral.Status.Conditions, astral.Generation,
			corev1alpha1.ConditionDependenciesReady, metav1.ConditionTrue, reasonDependenciesReady,
			"Every dependency is Ready")
	}

	// Orchestration only runs when the Astral is idle and unblocked: chaining a
	// second run onto a live one is exactly the concurrent-apply hazard the
	// per-state lock exists to prevent.
	requeue := r.requeue()
	if active == nil && len(pending) == 0 && !galaxy.Spec.Archived {
		next, err := r.orchestrate(ctx, &astral, flares)
		if err != nil {
			return r.fail(ctx, &astral, corev1alpha1.ConditionReconciled, reasonReconcileFailed, err)
		}
		if next > 0 && next < requeue {
			requeue = next
		}
	}

	r.markPhase(&astral, active, pending)

	logger.Info("✨ Astral reconciled",
		"astral", astral.Name,
		"galaxy", astral.Spec.GalaxyRef,
		"phase", astral.Status.Phase,
		"drift", astral.Status.Drift != nil && astral.Status.Drift.Detected,
		"lastSuccessfulFlare", astral.Status.LastSuccessfulFlare,
	)

	if err := r.updateStatus(ctx, &astral); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// resolveGalaxy loads the parent Galaxy and checks the Universe reference lines
// up with it, so an Astral cannot claim a tenant its Galaxy does not belong to.
func (r *AstralReconciler) resolveGalaxy(
	ctx context.Context,
	astral *corev1alpha1.Astral,
) (*corev1alpha1.Galaxy, error) {
	var galaxy corev1alpha1.Galaxy
	key := types.NamespacedName{Namespace: astral.Namespace, Name: astral.Spec.GalaxyRef}
	if err := r.Get(ctx, key, &galaxy); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Galaxy %q does not exist in namespace %q", astral.Spec.GalaxyRef, astral.Namespace)
		}
		return nil, fmt.Errorf("failed to read Galaxy %q: %w", astral.Spec.GalaxyRef, err)
	}
	if galaxy.Spec.UniverseRef != astral.Spec.UniverseRef {
		return nil, fmt.Errorf("Astral claims Universe %q but Galaxy %q belongs to %q",
			astral.Spec.UniverseRef, galaxy.Name, galaxy.Spec.UniverseRef)
	}
	return &galaxy, nil
}

// adopt stamps the hierarchy labels, the owner reference and the finalizer.
func (r *AstralReconciler) adopt(
	ctx context.Context,
	astral *corev1alpha1.Astral,
	galaxy *corev1alpha1.Galaxy,
) error {
	before := astral.DeepCopy()

	if astral.Labels == nil {
		astral.Labels = map[string]string{}
	}
	maps.Copy(astral.Labels, galaxy.Spec.Labels)
	astral.Labels[corev1alpha1.LabelUniverse] = astral.Spec.UniverseRef
	astral.Labels[corev1alpha1.LabelGalaxy] = galaxy.Name
	controllerutil.AddFinalizer(astral, corev1alpha1.AstralFinalizer)
	if err := controllerutil.SetControllerReference(galaxy, astral, r.Scheme); err != nil {
		return fmt.Errorf("failed to set Galaxy ownership on Astral: %w", err)
	}

	if equalMeta(before, astral) {
		return nil
	}
	if err := r.Update(ctx, astral); err != nil {
		return fmt.Errorf("failed to adopt Astral into Galaxy %q: %w", galaxy.Name, err)
	}
	return nil
}

// validate checks the parts of the configuration that can be verified without
// cloning the repository or running the engine.
func (r *AstralReconciler) validate(
	ctx context.Context,
	astral *corev1alpha1.Astral,
	galaxy *corev1alpha1.Galaxy,
) error {
	repo := astral.Spec.VCSRepository
	if strings.TrimSpace(repo.URL) == "" {
		return errors.New("spec.vcsRepository.url is empty")
	}
	if strings.HasPrefix(repo.Path, "/") {
		return fmt.Errorf("spec.vcsRepository.path %q must be relative to the repository root", repo.Path)
	}
	if strings.Contains(repo.Path, "..") {
		return fmt.Errorf("spec.vcsRepository.path %q must not escape the repository root", repo.Path)
	}
	if repo.SecretRef == nil && galaxy.Spec.VCSSecretRef == nil && requiresCredentials(repo.URL) {
		return fmt.Errorf("repository %q needs credentials: set spec.vcsRepository.secretRef "+
			"or the Galaxy's spec.vcsSecretRef", repo.URL)
	}

	for _, ref := range astral.Spec.Secrets {
		if err := checkSecret(ctx, r.Client, astral.Namespace, ref.Name); err != nil {
			return err
		}
	}
	if repo.SecretRef != nil {
		if err := checkSecret(ctx, r.Client, astral.Namespace, repo.SecretRef.Name); err != nil {
			return err
		}
	}
	return nil
}

// requiresCredentials reports whether a clone URL is private-by-default. A
// plain HTTPS URL may be public, so it is not treated as requiring auth.
func requiresCredentials(url string) bool {
	return strings.HasPrefix(url, "git@") || strings.HasPrefix(url, "ssh://")
}

// listFlares returns the Flares targeting this Astral, newest first.
func (r *AstralReconciler) listFlares(
	ctx context.Context,
	astral *corev1alpha1.Astral,
) ([]corev1alpha1.Flare, error) {
	var list corev1alpha1.FlareList
	if err := r.List(ctx, &list,
		client.InNamespace(astral.Namespace),
		client.MatchingFields{IndexFlareAstralRef: astral.Name},
	); err != nil {
		return nil, fmt.Errorf("failed to list Flares: %w", err)
	}

	flares := list.Items
	slices.SortFunc(flares, func(a, b corev1alpha1.Flare) int {
		return b.CreationTimestamp.Time.Compare(a.CreationTimestamp.Time)
	})
	return flares, nil
}

// observeFlares folds the Flare population into the Astral status and returns
// the one currently running, if any.
func (r *AstralReconciler) observeFlares(
	astral *corev1alpha1.Astral,
	flares []corev1alpha1.Flare,
) *corev1alpha1.Flare {
	var active *corev1alpha1.Flare
	history := make([]corev1alpha1.FlareRef, 0, corev1alpha1.MaxFlareHistory)
	lastSuccessful := ""
	var lastStatus corev1alpha1.FlarePhase

	for i := range flares {
		flare := &flares[i]
		if !flare.Status.Phase.IsTerminal() {
			if active == nil && flare.Status.Phase == corev1alpha1.FlarePhaseRunning {
				active = flare
			}
			continue
		}
		if lastStatus == "" {
			lastStatus = flare.Status.Phase
		}
		if lastSuccessful == "" && flare.Status.Phase == corev1alpha1.FlarePhaseSucceeded {
			lastSuccessful = flare.Name
		}
		if len(history) < corev1alpha1.MaxFlareHistory {
			history = append(history, corev1alpha1.FlareRef{
				Name:           flare.Name,
				Action:         flare.Spec.Action,
				Phase:          flare.Status.Phase,
				CompletionTime: flare.Status.CompletionTime,
			})
		}
		r.foldDrift(astral, flare)
		r.foldInfrastructure(astral, flare)
	}

	astral.Status.FlareHistory = history
	astral.Status.LastSuccessfulFlare = lastSuccessful
	astral.Status.LastFlareStatus = lastStatus
	return active
}

// foldDrift updates the drift status from a finished Refresh Flare. Only the
// most recent one matters, and flares arrive newest first.
func (r *AstralReconciler) foldDrift(astral *corev1alpha1.Astral, flare *corev1alpha1.Flare) {
	if flare.Spec.Action != corev1alpha1.FlareActionRefresh ||
		flare.Status.Phase != corev1alpha1.FlarePhaseSucceeded {
		return
	}
	if astral.Status.Drift != nil && astral.Status.Drift.LastChecked != nil &&
		!flare.Status.CompletionTime.Time.After(astral.Status.Drift.LastChecked.Time) {
		return
	}

	drifted := hasChanges(flare.Status.Resources)
	drift := &corev1alpha1.DriftStatus{Detected: drifted, LastChecked: flare.Status.CompletionTime}
	if drifted {
		drift.LastDetected = flare.Status.CompletionTime
	} else if astral.Status.Drift != nil {
		drift.LastDetected = astral.Status.Drift.LastDetected
	}
	astral.Status.Drift = drift
}

// foldInfrastructure refreshes the resource census from the newest Flare that
// reported one.
func (r *AstralReconciler) foldInfrastructure(astral *corev1alpha1.Astral, flare *corev1alpha1.Flare) {
	if astral.Status.Infrastructure != nil || flare.Status.Resources == nil {
		return
	}
	if flare.Status.Phase != corev1alpha1.FlarePhaseSucceeded {
		return
	}
	counts := flare.Status.Resources
	astral.Status.Infrastructure = &corev1alpha1.InfrastructureStatus{
		ResourceCount: counts.Unchanged + counts.Created + counts.Updated,
	}
}

// hasChanges reports whether a Flare reported a non-empty delta.
func hasChanges(counts *corev1alpha1.FlareResourceCounts) bool {
	return counts != nil && (counts.Created > 0 || counts.Updated > 0 || counts.Deleted > 0)
}

// dependenciesReady returns the names of the dependencies that are not Ready.
func (r *AstralReconciler) dependenciesReady(
	ctx context.Context,
	astral *corev1alpha1.Astral,
) ([]string, error) {
	var pending []string
	for _, name := range astral.Spec.DependsOn {
		var dependency corev1alpha1.Astral
		key := types.NamespacedName{Namespace: astral.Namespace, Name: name}
		if err := r.Get(ctx, key, &dependency); err != nil {
			if apierrors.IsNotFound(err) {
				pending = append(pending, name+" (missing)")
				continue
			}
			return nil, fmt.Errorf("failed to read dependency %q: %w", name, err)
		}
		if dependency.Status.Phase != corev1alpha1.AstralPhaseReady {
			pending = append(pending, name)
		}
	}
	return pending, nil
}

// orchestrate raises the Flares the Astral owes: an auto-apply chained onto a
// non-empty plan, and the periodic drift check. It returns the delay after
// which the next drift check is due, or zero when drift detection is off.
func (r *AstralReconciler) orchestrate(
	ctx context.Context,
	astral *corev1alpha1.Astral,
	flares []corev1alpha1.Flare,
) (time.Duration, error) {
	if err := r.chainAutoApply(ctx, astral, flares); err != nil {
		return 0, err
	}
	return r.scheduleDriftCheck(ctx, astral, flares)
}

// chainAutoApply raises an Apply for the newest successful Plan that produced
// changes and has not been chained yet.
func (r *AstralReconciler) chainAutoApply(
	ctx context.Context,
	astral *corev1alpha1.Astral,
	flares []corev1alpha1.Flare,
) error {
	if !astral.Spec.AutoApply {
		return nil
	}

	source := newestChainablePlan(flares)
	if source == nil {
		return nil
	}

	apply := &corev1alpha1.Flare{
		ObjectMeta: metav1.ObjectMeta{
			Name:      truncateName(source.Name) + "-apply",
			Namespace: astral.Namespace,
			Labels:    managedLabels(astral.Spec.UniverseRef, astral.Spec.GalaxyRef, astral.Name),
			Annotations: map[string]string{
				AnnotationSourceFlare: source.Name,
				AnnotationTrigger:     triggerAutoApply,
			},
		},
		Spec: corev1alpha1.FlareSpec{
			UniverseRef: astral.Spec.UniverseRef,
			GalaxyRef:   astral.Spec.GalaxyRef,
			AstralRef:   astral.Name,
			Action:      corev1alpha1.FlareActionApply,
			Variables:   source.Spec.Variables,
			RequestedBy: source.Spec.RequestedBy,
			Parallelism: source.Spec.EffectiveParallelism(),
		},
	}
	if err := controllerutil.SetControllerReference(astral, apply, r.Scheme); err != nil {
		return fmt.Errorf("failed to own chained Flare: %w", err)
	}

	if err := r.Create(ctx, apply); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("failed to chain auto-apply Flare: %w", err)
	}
	r.event(astral, corev1.EventTypeNormal, reasonFlareChainCreated,
		"Chained Apply Flare "+apply.Name+" from plan "+source.Name)
	return nil
}

// newestChainablePlan returns the most recent successful Plan with a non-empty
// delta, or nil when the newest terminal Flare is not such a plan. Looking only
// at the newest one is deliberate: an older plan is stale, and re-applying it
// would fight whatever ran after it.
func newestChainablePlan(flares []corev1alpha1.Flare) *corev1alpha1.Flare {
	for i := range flares {
		flare := &flares[i]
		if !flare.Status.Phase.IsTerminal() {
			continue
		}
		if flare.Spec.Action != corev1alpha1.FlareActionPlan ||
			flare.Status.Phase != corev1alpha1.FlarePhaseSucceeded ||
			!hasChanges(flare.Status.Resources) {
			return nil
		}
		if alreadyChained(flares, flare.Name) {
			return nil
		}
		return flare
	}
	return nil
}

// alreadyChained reports whether some Flare already descends from source.
func alreadyChained(flares []corev1alpha1.Flare, source string) bool {
	return slices.ContainsFunc(flares, func(f corev1alpha1.Flare) bool {
		return f.Annotations[AnnotationSourceFlare] == source
	})
}

// scheduleDriftCheck raises a Refresh Flare when the drift window has elapsed,
// and returns the delay until the next one is due.
func (r *AstralReconciler) scheduleDriftCheck(
	ctx context.Context,
	astral *corev1alpha1.Astral,
	flares []corev1alpha1.Flare,
) (time.Duration, error) {
	interval := astral.Spec.DriftInterval(metav1.Duration{Duration: DefaultDriftInterval}).Duration
	if interval <= 0 {
		return 0, nil
	}

	now := r.now()
	last := lastDriftCheck(astral, flares)
	if !last.IsZero() && now.Sub(last) < interval {
		return interval - now.Sub(last), nil
	}

	// The name buckets the timestamp by the drift window, so a status write
	// that never lands cannot make the controller raise two checks in one window.
	window := now.Unix() / int64(interval.Seconds())
	refresh := &corev1alpha1.Flare{
		ObjectMeta: metav1.ObjectMeta{
			Name:        fmt.Sprintf("%s-drift-%d", truncateName(astral.Name), window),
			Namespace:   astral.Namespace,
			Labels:      managedLabels(astral.Spec.UniverseRef, astral.Spec.GalaxyRef, astral.Name),
			Annotations: map[string]string{AnnotationTrigger: triggerDriftDetection},
		},
		Spec: corev1alpha1.FlareSpec{
			UniverseRef: astral.Spec.UniverseRef,
			GalaxyRef:   astral.Spec.GalaxyRef,
			AstralRef:   astral.Name,
			Action:      corev1alpha1.FlareActionRefresh,
			AutoApprove: true,
			Parallelism: corev1alpha1.DefaultFlareParallelism,
		},
	}
	if err := controllerutil.SetControllerReference(astral, refresh, r.Scheme); err != nil {
		return 0, fmt.Errorf("failed to own drift Flare: %w", err)
	}

	if err := r.Create(ctx, refresh); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return interval, nil
		}
		return 0, fmt.Errorf("failed to schedule drift check: %w", err)
	}
	r.event(astral, corev1.EventTypeNormal, reasonDriftFlareCreated,
		"Scheduled drift check Flare "+refresh.Name)
	return interval, nil
}

// lastDriftCheck returns when drift was last checked, preferring the status and
// falling back to the newest Refresh Flare so a lost status write only costs
// one extra check.
func lastDriftCheck(astral *corev1alpha1.Astral, flares []corev1alpha1.Flare) time.Time {
	if astral.Status.Drift != nil && astral.Status.Drift.LastChecked != nil {
		return astral.Status.Drift.LastChecked.Time
	}
	for i := range flares {
		if flares[i].Spec.Action == corev1alpha1.FlareActionRefresh {
			return flares[i].CreationTimestamp.Time
		}
	}
	return time.Time{}
}

// markPhase derives the coarse phase and the drift condition.
func (r *AstralReconciler) markPhase(
	astral *corev1alpha1.Astral,
	active *corev1alpha1.Flare,
	pending []string,
) {
	switch {
	case active != nil && active.Spec.Action.Mutates():
		astral.Status.Phase = corev1alpha1.AstralPhaseApplying
	case active != nil:
		astral.Status.Phase = corev1alpha1.AstralPhasePlanning
	case len(pending) > 0:
		astral.Status.Phase = corev1alpha1.AstralPhasePending
	case astral.Status.Drift != nil && astral.Status.Drift.Detected:
		astral.Status.Phase = corev1alpha1.AstralPhaseDrifted
	case astral.Status.LastFlareStatus == corev1alpha1.FlarePhaseFailed:
		astral.Status.Phase = corev1alpha1.AstralPhaseFailed
	default:
		astral.Status.Phase = corev1alpha1.AstralPhaseReady
	}

	switch {
	case astral.Status.Drift == nil:
		applyCondition(&astral.Status.Conditions, astral.Generation,
			corev1alpha1.ConditionDriftFree, metav1.ConditionUnknown, reasonDriftUnknown,
			"No drift check has completed yet")
	case astral.Status.Drift.Detected:
		applyCondition(&astral.Status.Conditions, astral.Generation,
			corev1alpha1.ConditionDriftFree, metav1.ConditionFalse, reasonDriftDetected,
			"Real infrastructure diverges from the tracked revision")
	default:
		applyCondition(&astral.Status.Conditions, astral.Generation,
			corev1alpha1.ConditionDriftFree, metav1.ConditionTrue, reasonDriftFree,
			"Last refresh found no drift")
	}

	reason, message := reasonReconciled, "Astral is ready"
	status := metav1.ConditionTrue
	switch astral.Status.Phase {
	case corev1alpha1.AstralPhaseFailed:
		status, reason, message = metav1.ConditionFalse, reasonLastFlareFailed, "The last Flare failed"
	case corev1alpha1.AstralPhaseDrifted:
		reason, message = reasonDriftDetected, "Astral is drifting"
	case corev1alpha1.AstralPhasePending:
		status, reason, message = metav1.ConditionFalse, reasonDependenciesUnmet, "Astral is waiting on dependencies"
	case corev1alpha1.AstralPhasePlanning, corev1alpha1.AstralPhaseApplying:
		reason, message = reasonFlareRunning, "A Flare is running against this Astral"
	case corev1alpha1.AstralPhaseReady:
		if astral.Status.LastFlareStatus == corev1alpha1.FlarePhaseSucceeded {
			reason = reasonLastFlareSucceeded
		}
	}
	applyCondition(&astral.Status.Conditions, astral.Generation,
		corev1alpha1.ConditionReconciled, status, reason, message)
}

// finalize holds the Astral until its Flares are reclaimed, so a delete never
// races a live engine process.
func (r *AstralReconciler) finalize(ctx context.Context, astral *corev1alpha1.Astral) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(astral, corev1alpha1.AstralFinalizer) {
		return ctrl.Result{}, nil
	}

	flares, err := r.listFlares(ctx, astral)
	if err != nil {
		return ctrl.Result{}, err
	}
	for i := range flares {
		if !flares[i].Status.Phase.IsTerminal() {
			logger.V(1).Info("Waiting for a running Flare before releasing Astral",
				"astral", astral.Name, "flare", flares[i].Name)
			return ctrl.Result{RequeueAfter: galaxyDrainRequeue}, nil
		}
	}

	controllerutil.RemoveFinalizer(astral, corev1alpha1.AstralFinalizer)
	if err := r.Update(ctx, astral); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to remove Astral finalizer: %w", err)
	}
	logger.Info("✨ Astral faded", "astral", astral.Name)
	return ctrl.Result{}, nil
}

func (r *AstralReconciler) fail(
	ctx context.Context,
	astral *corev1alpha1.Astral,
	condType, reason string,
	cause error,
) (ctrl.Result, error) {
	astral.Status.Phase = corev1alpha1.AstralPhaseFailed
	applyCondition(&astral.Status.Conditions, astral.Generation,
		condType, metav1.ConditionFalse, reason, cause.Error())
	applyCondition(&astral.Status.Conditions, astral.Generation,
		corev1alpha1.ConditionReconciled, metav1.ConditionFalse, reasonReconcileFailed, cause.Error())
	r.event(astral, corev1.EventTypeWarning, reason, cause.Error())

	if err := r.updateStatus(ctx, astral); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, cause
}

func (r *AstralReconciler) updateStatus(ctx context.Context, astral *corev1alpha1.Astral) error {
	astral.Status.ObservedGeneration = astral.Generation
	if err := r.Status().Update(ctx, astral); err != nil {
		return fmt.Errorf("failed to update Astral status: %w", err)
	}
	return nil
}

func (r *AstralReconciler) event(astral *corev1alpha1.Astral, eventType, reason, message string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Event(astral, eventType, reason, message)
}

func (r *AstralReconciler) requeue() time.Duration {
	if r.RequeueInterval > 0 {
		return r.RequeueInterval
	}
	return DefaultAstralRequeue
}

func (r *AstralReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// truncateName keeps a generated child name within the Kubernetes name limit.
func truncateName(name string) string {
	if len(name) <= maxNameLength {
		return name
	}
	return name[:maxNameLength]
}

// SetupWithManager wires the Astral controller.
func (r *AstralReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.Astral{}).
		Owns(&corev1alpha1.Flare{}).
		Watches(&corev1alpha1.Flare{}, handler.EnqueueRequestsFromMapFunc(mapFlareToAstral)).
		Named("astral").
		Complete(r)
}

// mapFlareToAstral routes a Flare back to the Astral named in its spec. It
// complements Owns() so a Flare created by hand, without an owner reference,
// still wakes its Astral.
func mapFlareToAstral(_ context.Context, obj client.Object) []reconcile.Request {
	flare, ok := obj.(*corev1alpha1.Flare)
	if !ok || flare.Spec.AstralRef == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Namespace: flare.Namespace, Name: flare.Spec.AstralRef},
	}}
}
