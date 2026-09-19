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
	"os"
	"path/filepath"
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
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
	"github.com/nrx-ops/stellarCD/internal/controller/shared"
)

// runPollInterval paces the reconciler while an engine process is live. The run
// itself happens on its own goroutine, so this only costs a cheap status read.
const runPollInterval = 5 * time.Second

// lockRetryInterval paces the retry when another Flare holds the state lock.
const lockRetryInterval = 15 * time.Second

// maxArtifactBytes caps what is stored in an artifact Secret. etcd rejects
// values above 1 MiB, so a larger plan file is reported rather than stored.
const maxArtifactBytes = 768 << 10

// maxStatusOutput caps the plan/apply summary kept on the status.
const maxStatusOutput = 8 << 10

// Flare-specific condition reasons.
const (
	reasonAwaitingApproval = "AwaitingApproval"
	reasonApproved         = "Approved"
	reasonRejected         = "Rejected"
	reasonCancelled        = "Cancelled"
	reasonLockBusy         = "StateLockBusy"
	reasonRunStarted       = "RunStarted"
	reasonRunSucceeded     = "RunSucceeded"
	reasonRunFailed        = "RunFailed"
	reasonTimedOut         = "TimedOut"
	reasonAstralNotReady   = "AstralNotReady"
)

// FlareReconciler reconciles a Flare: one burst of engine execution, gated by
// approval, serialised per state and bounded by a timeout.
type FlareReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// Executor runs the engine. Defaults to a real os/exec executor.
	Executor shared.Executor
	// Workspace materialises the checkout. Defaults to a git CLI provider.
	Workspace shared.WorkspaceProvider
	// Locks serialises runs per Astral state.
	Locks *shared.StateLock
	// Runs tracks the goroutines executing live Flares.
	Runs *RunRegistry
	// ArtifactRetention bounds how long a plan artifact Secret is kept. Zero
	// keeps it for the lifetime of the Flare.
	ArtifactRetention time.Duration
}

// +kubebuilder:rbac:groups=core.stellarcd.io,resources=flares,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core.stellarcd.io,resources=flares/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.stellarcd.io,resources=flares/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives one Flare from Pending to a terminal phase. The engine runs
// on its own goroutine so a long apply never pins a reconcile worker; each pass
// either starts the run, observes it, or records its outcome.
func (r *FlareReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var flare corev1alpha1.Flare
	if err := r.Get(ctx, req.NamespacedName, &flare); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !flare.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &flare)
	}
	if flare.Status.Phase.IsTerminal() {
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&flare, corev1alpha1.FlareFinalizer) {
		controllerutil.AddFinalizer(&flare, corev1alpha1.FlareFinalizer)
		if err := r.Update(ctx, &flare); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add Flare finalizer: %w", err)
		}
		return ctrl.Result{}, nil
	}

	if run := r.registry().Get(flare.UID); run != nil {
		return r.observeRun(ctx, &flare, run)
	}
	if flare.Spec.Cancel {
		return r.terminate(ctx, &flare, corev1alpha1.FlarePhaseCancelled, reasonCancelled,
			"Run cancelled before it started")
	}

	astral, galaxy, err := r.resolveTargets(ctx, &flare)
	if err != nil {
		return r.fail(ctx, &flare, reasonParentMissing, err)
	}

	if hold, reason, message := r.approvalHold(&flare, galaxy); hold {
		return r.awaitApproval(ctx, &flare, reason, message)
	}
	applyCondition(&flare.Status.Conditions, flare.Generation,
		corev1alpha1.ConditionApproved, metav1.ConditionTrue, reasonApproved, "Run is cleared to start")

	lockKey := StateLockKey(&flare)
	holder := string(flare.UID)
	if astral.Spec.EnableLocking && !r.locks().TryAcquire(lockKey, holder) {
		applyCondition(&flare.Status.Conditions, flare.Generation,
			corev1alpha1.ConditionStarted, metav1.ConditionFalse, reasonLockBusy,
			"Another Flare holds the state lock for "+lockKey)
		flare.Status.Phase = corev1alpha1.FlarePhasePending
		if err := r.updateStatus(ctx, &flare); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: lockRetryInterval}, nil
	}

	if err := r.startRun(ctx, &flare, astral, galaxy); err != nil {
		r.locks().Release(lockKey, holder)
		return r.fail(ctx, &flare, reasonRunFailed, err)
	}

	logger.Info("🔥 Flare ignited",
		"flare", flare.Name,
		"astral", flare.Spec.AstralRef,
		"galaxy", flare.Spec.GalaxyRef,
		"action", flare.Spec.Action,
		"requestedBy", flare.Spec.RequestedBy,
	)
	return ctrl.Result{RequeueAfter: runPollInterval}, nil
}

// resolveTargets loads the Astral and the Galaxy this Flare runs against, and
// checks the whole chain agrees on who owns whom.
func (r *FlareReconciler) resolveTargets(
	ctx context.Context,
	flare *corev1alpha1.Flare,
) (*corev1alpha1.Astral, *corev1alpha1.Galaxy, error) {
	var astral corev1alpha1.Astral
	key := types.NamespacedName{Namespace: flare.Namespace, Name: flare.Spec.AstralRef}
	if err := r.Get(ctx, key, &astral); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, fmt.Errorf("Astral %q does not exist in namespace %q",
				flare.Spec.AstralRef, flare.Namespace)
		}
		return nil, nil, fmt.Errorf("failed to read Astral %q: %w", flare.Spec.AstralRef, err)
	}
	if astral.Spec.GalaxyRef != flare.Spec.GalaxyRef {
		return nil, nil, fmt.Errorf("Flare targets Galaxy %q but Astral %q belongs to %q",
			flare.Spec.GalaxyRef, astral.Name, astral.Spec.GalaxyRef)
	}

	var galaxy corev1alpha1.Galaxy
	galaxyKey := types.NamespacedName{Namespace: flare.Namespace, Name: flare.Spec.GalaxyRef}
	if err := r.Get(ctx, galaxyKey, &galaxy); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, fmt.Errorf("Galaxy %q does not exist in namespace %q",
				flare.Spec.GalaxyRef, flare.Namespace)
		}
		return nil, nil, fmt.Errorf("failed to read Galaxy %q: %w", flare.Spec.GalaxyRef, err)
	}
	if galaxy.Spec.Archived {
		return nil, nil, fmt.Errorf("Galaxy %q is archived", galaxy.Name)
	}
	if !conditionTrue(astral.Status.Conditions, corev1alpha1.ConditionConfigValid) {
		return nil, nil, fmt.Errorf("Astral %q has not reported a valid configuration yet", astral.Name)
	}
	return &astral, &galaxy, nil
}

// approvalHold decides whether the run must wait for a human. It reports the
// hold, the condition reason and the message.
func (r *FlareReconciler) approvalHold(
	flare *corev1alpha1.Flare,
	galaxy *corev1alpha1.Galaxy,
) (bool, string, string) {
	if flare.Spec.Approved != nil && !*flare.Spec.Approved {
		return true, reasonRejected, "Run was explicitly rejected"
	}
	if flare.Spec.Approved != nil && *flare.Spec.Approved {
		return false, "", ""
	}
	if flare.Spec.AutoApprove && galaxy.Spec.ApprovalPolicy != corev1alpha1.ApprovalPolicyManual {
		return false, "", ""
	}
	if !galaxy.Spec.RequiresApproval(flare.Spec.Action) {
		return false, "", ""
	}
	return true, reasonAwaitingApproval, fmt.Sprintf(
		"Galaxy approval policy %s holds a %s run until spec.approved is set",
		galaxy.Spec.ApprovalPolicy, flare.Spec.Action)
}

// awaitApproval parks the Flare. An explicit rejection is terminal; a pending
// review simply waits for the next spec change.
func (r *FlareReconciler) awaitApproval(
	ctx context.Context,
	flare *corev1alpha1.Flare,
	reason, message string,
) (ctrl.Result, error) {
	if reason == reasonRejected {
		return r.terminate(ctx, flare, corev1alpha1.FlarePhaseCancelled, reasonRejected, message)
	}

	flare.Status.Phase = corev1alpha1.FlarePhaseAwaitingApproval
	flare.Status.Action = flare.Spec.Action
	applyCondition(&flare.Status.Conditions, flare.Generation,
		corev1alpha1.ConditionApproved, metav1.ConditionFalse, reason, message)
	r.event(flare, corev1.EventTypeNormal, reason, message)

	if err := r.updateStatus(ctx, flare); err != nil {
		return ctrl.Result{}, err
	}
	// No requeue: approval arrives as a spec update, which the watch delivers.
	return ctrl.Result{}, nil
}

// startRun assembles the run request and launches it on its own goroutine.
func (r *FlareReconciler) startRun(
	ctx context.Context,
	flare *corev1alpha1.Flare,
	astral *corev1alpha1.Astral,
	galaxy *corev1alpha1.Galaxy,
) error {
	credentials, err := r.vcsCredentials(ctx, astral, galaxy)
	if err != nil {
		return err
	}
	env, err := r.runEnvironment(ctx, astral, galaxy)
	if err != nil {
		return err
	}

	checkoutReq := shared.CheckoutRequest{
		Key:         filepath.Join(flare.Spec.UniverseRef, flare.Spec.GalaxyRef, astral.Name),
		URL:         astral.Spec.VCSRepository.URL,
		Branch:      astral.Spec.VCSRepository.Branch,
		Path:        astral.Spec.VCSRepository.Path,
		Credentials: credentials,
	}
	runReq := shared.RunRequest{
		Engine:            engineFor(galaxy),
		Action:            shared.Action(flare.Spec.Action),
		Workspace:         astral.Spec.WorkspaceName,
		Parallelism:       flare.Spec.EffectiveParallelism(),
		Lock:              astral.Spec.EnableLocking,
		RefreshBeforePlan: flare.Spec.RefreshBeforePlan,
		Variables:         mergedVariables(astral, flare),
		Env:               env,
		Hooks:             hooksFor(astral, flare.Spec.Action),
	}
	if flare.Spec.Action == corev1alpha1.FlareActionPlan {
		runReq.PlanFile = "stellarcd.tfplan"
	}

	flare.Status.Phase = corev1alpha1.FlarePhaseRunning
	flare.Status.Action = flare.Spec.Action
	flare.Status.StartTime = ptrTime(metav1.Now())
	applyCondition(&flare.Status.Conditions, flare.Generation,
		corev1alpha1.ConditionStarted, metav1.ConditionTrue, reasonRunStarted,
		"Engine run started for action "+string(flare.Spec.Action))
	if err := r.updateStatus(ctx, flare); err != nil {
		return err
	}
	r.event(flare, corev1.EventTypeNormal, reasonRunStarted, "Started "+string(flare.Spec.Action))

	r.registry().Launch(flare.UID, flare.Spec.Timeout().Duration, func(runCtx context.Context) (*RunOutcome, error) {
		return r.execute(runCtx, checkoutReq, runReq)
	})
	return nil
}

// execute prepares the checkout, runs the engine and collects the artifacts. It
// runs off the reconcile goroutine, so it takes no Kubernetes objects: every
// value it needs is already resolved into the request structs.
func (r *FlareReconciler) execute(
	ctx context.Context,
	checkoutReq shared.CheckoutRequest,
	runReq shared.RunRequest,
) (*RunOutcome, error) {
	checkout, err := r.workspace().Prepare(ctx, checkoutReq)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Credentials are shredded even when the run fails: they must not
		// survive on disk between Flares.
		_ = r.workspace().Cleanup(checkout)
	}()

	runReq.WorkDir = checkout.ModuleDir
	if runReq.Env == nil {
		runReq.Env = map[string]string{}
	}
	maps.Copy(runReq.Env, checkout.Env)

	result, runErr := r.executor().Run(ctx, runReq)
	outcome := &RunOutcome{Revision: checkout.Revision, Result: result}
	if runReq.PlanFile != "" && result != nil && runErr == nil {
		outcome.PlanArtifact = readArtifact(filepath.Join(checkout.ModuleDir, runReq.PlanFile))
	}
	return outcome, runErr
}

// observeRun folds a finished run into the status, or keeps waiting.
func (r *FlareReconciler) observeRun(
	ctx context.Context,
	flare *corev1alpha1.Flare,
	run *Run,
) (ctrl.Result, error) {
	if flare.Spec.Cancel && !run.Cancelled() {
		run.Cancel()
		r.event(flare, corev1.EventTypeWarning, reasonCancelled, "Cancellation requested; aborting the engine run")
		return ctrl.Result{RequeueAfter: runPollInterval}, nil
	}
	if !run.Done() {
		return ctrl.Result{RequeueAfter: runPollInterval}, nil
	}

	outcome, runErr := run.Result()
	r.registry().Forget(flare.UID)
	r.locks().Release(StateLockKey(flare), string(flare.UID))

	if outcome != nil && outcome.Result != nil {
		r.recordResult(flare, outcome)
	}

	switch {
	case run.TimedOut():
		return r.terminate(ctx, flare, corev1alpha1.FlarePhaseCancelled, reasonTimedOut,
			fmt.Sprintf("Run exceeded its %ds timeout and was killed", flare.Spec.TimeoutSeconds))
	case run.Cancelled():
		return r.terminate(ctx, flare, corev1alpha1.FlarePhaseCancelled, reasonCancelled,
			"Run was cancelled")
	case runErr != nil:
		return r.terminate(ctx, flare, corev1alpha1.FlarePhaseFailed, reasonRunFailed, runErr.Error())
	}

	if err := r.storeArtifacts(ctx, flare, outcome); err != nil {
		// Losing the plan file is not worth failing a successful apply over.
		flare.Status.Warnings = append(flare.Status.Warnings, err.Error())
	}
	return r.terminate(ctx, flare, corev1alpha1.FlarePhaseSucceeded, reasonRunSucceeded,
		"Engine run completed for action "+string(flare.Spec.Action))
}

// recordResult copies the engine output onto the status, truncated to keep the
// object small enough for etcd.
func (r *FlareReconciler) recordResult(flare *corev1alpha1.Flare, outcome *RunOutcome) {
	result := outcome.Result
	summary := clip(result.Output, maxStatusOutput)
	if flare.Spec.Action.Mutates() {
		flare.Status.ApplyOutput = summary
	} else {
		flare.Status.PlanOutput = summary
	}

	flare.Status.Resources = &corev1alpha1.FlareResourceCounts{
		Created:   result.Counts.Created,
		Updated:   result.Counts.Updated,
		Deleted:   result.Counts.Deleted,
		Unchanged: result.Counts.Unchanged,
	}
	flare.Status.Errors = result.Errors
	flare.Status.Warnings = result.Warnings
}

// storeArtifacts writes the binary plan into a Secret owned by the Flare. A
// plan file carries the same secrets as a state file, so a ConfigMap would be
// the wrong home for it.
func (r *FlareReconciler) storeArtifacts(
	ctx context.Context,
	flare *corev1alpha1.Flare,
	outcome *RunOutcome,
) error {
	if outcome == nil || len(outcome.PlanArtifact) == 0 {
		return nil
	}
	if len(outcome.PlanArtifact) > maxArtifactBytes {
		return fmt.Errorf("plan file is %d bytes, above the %d byte artifact limit; it was not stored",
			len(outcome.PlanArtifact), maxArtifactBytes)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      truncateName(flare.Name) + "-plan",
			Namespace: flare.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Labels = managedLabels(flare.Spec.UniverseRef, flare.Spec.GalaxyRef, flare.Spec.AstralRef)
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{"tfplan": outcome.PlanArtifact}
		return controllerutil.SetControllerReference(flare, secret, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("failed to store plan artifact: %w", err)
	}

	flare.Status.Artifacts = &corev1alpha1.FlareArtifacts{PlanFile: secret.Name}
	return nil
}

// terminate writes the final phase and stops the reconcile loop for this Flare.
func (r *FlareReconciler) terminate(
	ctx context.Context,
	flare *corev1alpha1.Flare,
	phase corev1alpha1.FlarePhase,
	reason, message string,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	flare.Status.Phase = phase
	flare.Status.Action = flare.Spec.Action
	flare.Status.CompletionTime = ptrTime(metav1.Now())
	if flare.Status.StartTime != nil {
		elapsed := flare.Status.CompletionTime.Sub(flare.Status.StartTime.Time)
		flare.Status.Duration = elapsed.Round(time.Second).String()
	}

	status := metav1.ConditionTrue
	eventType := corev1.EventTypeNormal
	if phase != corev1alpha1.FlarePhaseSucceeded {
		status, eventType = metav1.ConditionFalse, corev1.EventTypeWarning
		flare.Status.Errors = appendUnique(flare.Status.Errors, message)
	}
	applyCondition(&flare.Status.Conditions, flare.Generation,
		corev1alpha1.ConditionCompleted, status, reason, message)
	r.event(flare, eventType, reason, message)

	logger.Info("🔥 Flare settled",
		"flare", flare.Name,
		"astral", flare.Spec.AstralRef,
		"action", flare.Spec.Action,
		"phase", phase,
		"duration", flare.Status.Duration,
	)

	if err := r.updateStatus(ctx, flare); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// finalize aborts a live run before letting the Flare go, so a delete cannot
// leave an orphaned terraform process holding the remote state lock.
func (r *FlareReconciler) finalize(ctx context.Context, flare *corev1alpha1.Flare) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(flare, corev1alpha1.FlareFinalizer) {
		return ctrl.Result{}, nil
	}

	if run := r.registry().Get(flare.UID); run != nil {
		if !run.Done() {
			run.Cancel()
			logger.V(1).Info("Aborting engine run before Flare deletion", "flare", flare.Name)
			return ctrl.Result{RequeueAfter: runPollInterval}, nil
		}
		r.registry().Forget(flare.UID)
	}
	r.locks().Release(StateLockKey(flare), string(flare.UID))

	controllerutil.RemoveFinalizer(flare, corev1alpha1.FlareFinalizer)
	if err := r.Update(ctx, flare); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to remove Flare finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// vcsCredentials resolves the clone credentials, preferring the Astral's own
// Secret over the Galaxy default.
func (r *FlareReconciler) vcsCredentials(
	ctx context.Context,
	astral *corev1alpha1.Astral,
	galaxy *corev1alpha1.Galaxy,
) (map[string][]byte, error) {
	ref := astral.Spec.VCSRepository.SecretRef
	if ref == nil {
		ref = galaxy.Spec.VCSSecretRef
	}
	if ref == nil {
		return nil, nil
	}

	var secret corev1.Secret
	key := types.NamespacedName{Namespace: astral.Namespace, Name: ref.Name}
	if err := r.Get(ctx, key, &secret); err != nil {
		return nil, fmt.Errorf("failed to read VCS credentials Secret %q: %w", ref.Name, err)
	}
	return secret.Data, nil
}

// runEnvironment builds the engine environment: the backend credentials, the
// Astral's projected Secrets and the minimum a subprocess needs. The manager's
// own environment is deliberately not inherited, so the ServiceAccount token
// never reaches a provider plugin.
func (r *FlareReconciler) runEnvironment(
	ctx context.Context,
	astral *corev1alpha1.Astral,
	galaxy *corev1alpha1.Galaxy,
) (map[string]string, error) {
	env := map[string]string{
		"PATH":             os.Getenv("PATH"),
		"HOME":             os.Getenv("HOME"),
		"TF_IN_AUTOMATION": "1",
		"TF_INPUT":         "0",
	}

	if galaxy.Spec.BackendConfig != nil && galaxy.Spec.BackendConfig.SecretRef != nil {
		// Backend credentials keep their own key names: providers look for
		// AWS_ACCESS_KEY_ID and friends verbatim.
		if err := r.projectSecret(ctx, astral.Namespace,
			corev1alpha1.EnvSecretRef{Name: galaxy.Spec.BackendConfig.SecretRef.Name}, env); err != nil {
			return nil, err
		}
	}
	for _, ref := range astral.Spec.Secrets {
		if err := r.projectSecret(ctx, astral.Namespace, ref, env); err != nil {
			return nil, err
		}
	}
	return env, nil
}

// projectSecret copies Secret entries into the environment map under the
// requested prefix.
func (r *FlareReconciler) projectSecret(
	ctx context.Context,
	namespace string,
	ref corev1alpha1.EnvSecretRef,
	env map[string]string,
) error {
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, &secret); err != nil {
		return fmt.Errorf("failed to read Secret %q: %w", ref.Name, err)
	}

	if ref.Key != "" {
		value, ok := secret.Data[ref.Key]
		if !ok {
			return fmt.Errorf("Secret %q has no key %q", ref.Name, ref.Key)
		}
		env[ref.EnvPrefix+ref.Key] = string(value)
		return nil
	}
	for key, value := range secret.Data {
		env[ref.EnvPrefix+key] = string(value)
	}
	return nil
}

// mergedVariables layers the Flare overrides on top of the Astral defaults.
func mergedVariables(astral *corev1alpha1.Astral, flare *corev1alpha1.Flare) map[string]string {
	merged := make(map[string]string, len(astral.Spec.Variables)+len(flare.Spec.Variables))
	maps.Copy(merged, astral.Spec.Variables)
	maps.Copy(merged, flare.Spec.Variables)
	return merged
}

// hooksFor selects the hook pair matching the action.
func hooksFor(astral *corev1alpha1.Astral, action corev1alpha1.FlareAction) shared.Hooks {
	if astral.Spec.CustomHooks == nil {
		return shared.Hooks{}
	}
	hooks := astral.Spec.CustomHooks
	if action.Mutates() {
		return shared.Hooks{Pre: hooks.PreApply, Post: hooks.PostApply}
	}
	return shared.Hooks{Pre: hooks.PrePlan, Post: hooks.PostPlan}
}

// engineFor maps the Galaxy engine onto the executor engine.
func engineFor(galaxy *corev1alpha1.Galaxy) shared.Engine {
	switch galaxy.Spec.Terrarium.Engine {
	case corev1alpha1.TerrariumEngineOpenTofu:
		return shared.EngineOpenTofu
	case corev1alpha1.TerrariumEngineTerragrunt:
		return shared.EngineTerragrunt
	default:
		return shared.EngineTerraform
	}
}

// StateLockKey identifies the remote state a Flare will touch. Two Flares that
// share it must never run at the same time.
func StateLockKey(flare *corev1alpha1.Flare) string {
	return strings.Join([]string{flare.Namespace, flare.Spec.GalaxyRef, flare.Spec.AstralRef}, "/")
}

// readArtifact loads a produced artifact, returning nil when it is absent or
// unreadable: a missing plan file is a warning, not a failed run.
func readArtifact(path string) []byte {
	data, err := os.ReadFile(path) // #nosec G304 -- path is built from a validated module directory
	if err != nil {
		return nil
	}
	return data
}

// clip truncates a blob, keeping the tail where engine failures are reported.
func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return "...[truncated]...\n" + text[len(text)-limit:]
}

// appendUnique adds msg unless it is already present.
func appendUnique(list []string, msg string) []string {
	if msg == "" {
		return list
	}
	for _, existing := range list {
		if existing == msg {
			return list
		}
	}
	return append(list, msg)
}

func (r *FlareReconciler) fail(
	ctx context.Context,
	flare *corev1alpha1.Flare,
	reason string,
	cause error,
) (ctrl.Result, error) {
	flare.Status.Phase = corev1alpha1.FlarePhasePending
	applyCondition(&flare.Status.Conditions, flare.Generation,
		corev1alpha1.ConditionStarted, metav1.ConditionFalse, reason, cause.Error())
	r.event(flare, corev1.EventTypeWarning, reason, cause.Error())

	if err := r.updateStatus(ctx, flare); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, cause
}

func (r *FlareReconciler) updateStatus(ctx context.Context, flare *corev1alpha1.Flare) error {
	flare.Status.ObservedGeneration = flare.Generation
	if err := r.Status().Update(ctx, flare); err != nil {
		return fmt.Errorf("failed to update Flare status: %w", err)
	}
	return nil
}

func (r *FlareReconciler) event(flare *corev1alpha1.Flare, eventType, reason, message string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Event(flare, eventType, reason, message)
}

func (r *FlareReconciler) executor() shared.Executor {
	if r.Executor != nil {
		return r.Executor
	}
	return shared.NewCommandExecutor()
}

func (r *FlareReconciler) workspace() shared.WorkspaceProvider {
	if r.Workspace != nil {
		return r.Workspace
	}
	return shared.NewGitWorkspace(DefaultWorkspaceRoot)
}

func (r *FlareReconciler) locks() *shared.StateLock {
	if r.Locks == nil {
		r.Locks = shared.NewStateLock()
	}
	return r.Locks
}

func (r *FlareReconciler) registry() *RunRegistry {
	if r.Runs == nil {
		r.Runs = NewRunRegistry()
	}
	return r.Runs
}

// DefaultWorkspaceRoot is where checkouts land when no root is configured.
const DefaultWorkspaceRoot = "/var/lib/stellarcd/workspaces"

// SetupWithManager wires the Flare controller. Defaults are filled in here so a
// zero-value reconciler is still usable, e.g. from tests.
func (r *FlareReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Locks == nil {
		r.Locks = shared.NewStateLock()
	}
	if r.Runs == nil {
		r.Runs = NewRunRegistry()
	}
	if r.Executor == nil {
		r.Executor = shared.NewCommandExecutor()
	}
	if r.Workspace == nil {
		r.Workspace = shared.NewGitWorkspace(DefaultWorkspaceRoot)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.Flare{}).
		Owns(&corev1.Secret{}).
		Named("flare").
		Complete(r)
}

// errRunLost is returned when a registry entry finishes without a result, which
// can only happen if the run goroutine panicked.
var errRunLost = errors.New("engine run produced no result")
