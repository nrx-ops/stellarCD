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
	"net/http"
	"slices"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
	"github.com/nrx-ops/stellarCD/internal/controller/shared"
	"github.com/nrx-ops/stellarCD/internal/runspec"
)

// jobPollInterval paces the reconciler while a Job runs. Job status changes
// arrive through the watch; the poll is what picks up step markers in the log.
const jobPollInterval = 5 * time.Second

// lockRetryInterval paces the retry when another Flare holds the Astral's lock.
const lockRetryInterval = 15 * time.Second

// logTailLines is how much of the runner log is read. The result block sits at
// the very end; the step markers are scattered through the engine output.
const logTailLines = int64(2000)

// maxStatusOutput caps the plan/apply output kept on the status.
const maxStatusOutput = 8 << 10

// Flare-specific condition reasons.
const (
	reasonAwaitingApproval     = "AwaitingApproval"
	reasonApproved             = "Approved"
	reasonRejected             = "Rejected"
	reasonCancelled            = "Cancelled"
	reasonLockBusy             = "FlareAlreadyRunning"
	reasonRunStarted           = "RunStarted"
	reasonRunSucceeded         = "RunSucceeded"
	reasonRunFailed            = "RunFailed"
	reasonTimedOut             = "TimedOut"
	reasonAwaitingConfirmation = "AwaitingDestroyConfirmation"
	reasonDestroyConfirmed     = "DestroyConfirmed"
	reasonConfirmationMismatch = "ConfirmationMismatch"
	reasonRunRefused           = "RunRefused"
	reasonJobLost              = "JobLost"
)

// FlareReconciler drives one Flare: approval, the run lock, its Job, the
// destroy confirmation, and the result.
//
// The controller never runs an engine itself. Each attempt is a Kubernetes Job
// owned by the Flare, so a controller restart or a leader change leaves running
// applies alone: the next reconcile finds the Job again by name.
type FlareReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// APIReader reads Flares straight from the API server when taking the run
	// lock. The cache can lag behind a status write that another reconcile
	// just made, and a stale read there would let two Flares in at once.
	APIReader client.Reader
	// Logs reads the runner's log, where it reports its progress and result.
	Logs PodLogReader
	// Runner configures the Jobs.
	Runner RunnerConfig
	// HTTPClient exchanges GitHub App credentials for installation tokens.
	HTTPClient *http.Client
}

// +kubebuilder:rbac:groups=core.stellarcd.io,resources=flares,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core.stellarcd.io,resources=flares/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.stellarcd.io,resources=flares/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete

// Reconcile moves a Flare one step along: start its Job, observe it, or wait
// for a human.
func (r *FlareReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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

	if flare.Spec.Cancel {
		return r.cancel(ctx, &flare)
	}

	astral, galaxy, err := r.resolveTargets(ctx, &flare)
	if err != nil {
		return r.fail(ctx, &flare, reasonParentMissing, err)
	}

	switch flare.Status.Phase {
	case corev1alpha1.FlarePhaseRunning:
		return r.observe(ctx, &flare, astral, galaxy)
	case corev1alpha1.FlarePhaseAwaitingConfirmation:
		return r.awaitConfirmation(ctx, &flare, astral, galaxy)
	}

	if err := validateRun(&flare, astral); err != nil {
		return r.terminate(ctx, &flare, corev1alpha1.FlarePhaseFailed, reasonRunRefused, err.Error())
	}
	if hold, reason, message := r.approvalHold(&flare, galaxy); hold {
		return r.awaitApproval(ctx, &flare, reason, message)
	}
	applyCondition(&flare.Status.Conditions, flare.Generation,
		corev1alpha1.ConditionApproved, metav1.ConditionTrue, reasonApproved, "Run is cleared to start")

	return r.start(ctx, &flare, astral, galaxy, nil)
}

// validateRun refuses a run the configuration cannot honour, before anything
// is locked or launched.
func validateRun(flare *corev1alpha1.Flare, astral *corev1alpha1.Astral) error {
	runAll := astral.Spec.Terragrunt.RunAll()
	if flare.Spec.EffectiveScope() == corev1alpha1.FlareScopeUnit {
		if !runAll {
			return errors.New("scope Unit needs an Astral with Terragrunt in RunAll mode")
		}
		if strings.Contains(flare.Spec.UnitPath, "..") || strings.HasPrefix(flare.Spec.UnitPath, "/") {
			return fmt.Errorf("unitPath %q must stay inside the Astral path", flare.Spec.UnitPath)
		}
		return nil
	}
	// RG-Run-all blocked by a failed unit: a whole-Astral apply waits until
	// every unit plans cleanly. Single-unit runs and plans stay possible.
	if runAll && flare.Spec.Action.Mutates() {
		if failed := failedUnitPaths(astral); len(failed) > 0 {
			return fmt.Errorf("Disabled: %d unit(s) failed (%s). Fix them first",
				len(failed), strings.Join(failed, ", "))
		}
	}
	return nil
}

func failedUnitPaths(astral *corev1alpha1.Astral) []string {
	var failed []string
	for _, u := range astral.Status.Units {
		if u.Result == corev1alpha1.UnitResultFailed {
			failed = append(failed, u.Path)
		}
	}
	return failed
}

// start takes the Astral's run lock and launches the Flare's next Job. The lock
// is persisted before the Job is created: if the controller dies in between,
// the Flare already owns the lock and the next reconcile simply creates the
// Job, under the same deterministic name.
func (r *FlareReconciler) start(
	ctx context.Context,
	flare *corev1alpha1.Flare,
	astral *corev1alpha1.Astral,
	galaxy *corev1alpha1.Galaxy,
	allowedDestroy []string,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !flare.HoldsLock() {
		holder, err := r.lockHolder(ctx, flare)
		if err != nil {
			return ctrl.Result{}, err
		}
		if holder != "" {
			flare.Status.Phase = corev1alpha1.FlarePhasePending
			flare.Status.Message = "Waiting: Flare " + holder + " is already running on this Astral"
			applyCondition(&flare.Status.Conditions, flare.Generation,
				corev1alpha1.ConditionStarted, metav1.ConditionFalse, reasonLockBusy,
				"A Flare is already running on this Astral: "+holder)
			if err := r.updateStatus(ctx, flare); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: lockRetryInterval}, nil
		}

		now := metav1.Now()
		flare.Status.LockAcquiredAt = &now
		flare.Status.Attempt++
		flare.Status.JobName = ""
		flare.Status.Phase = corev1alpha1.FlarePhaseRunning
		flare.Status.Action = flare.Spec.Action
		if flare.Status.StartTime == nil {
			flare.Status.StartTime = &now
		}
		if err := r.updateStatus(ctx, flare); err != nil {
			// A conflict means someone else wrote first; the requeue retries
			// against the fresh object, lock check included.
			return ctrl.Result{}, err
		}
	}

	job, err := r.ensureJob(ctx, flare, astral, galaxy, allowedDestroy)
	if err != nil {
		return r.terminate(ctx, flare, corev1alpha1.FlarePhaseFailed, reasonRunFailed, err.Error())
	}

	flare.Status.JobName = job.Name
	flare.Status.Step = corev1alpha1.FlareStepGitFetch
	flare.Status.Message = "Job " + job.Name + " started"
	applyCondition(&flare.Status.Conditions, flare.Generation,
		corev1alpha1.ConditionStarted, metav1.ConditionTrue, reasonRunStarted,
		fmt.Sprintf("Job %s started for action %s", job.Name, flare.Spec.Action))
	if err := r.updateStatus(ctx, flare); err != nil {
		return ctrl.Result{}, err
	}
	r.event(flare, corev1.EventTypeNormal, reasonRunStarted, "Started Job "+job.Name)

	logger.Info("🔥 Flare ignited",
		"flare", flare.Name,
		"astral", flare.Spec.AstralRef,
		"action", flare.Spec.Action,
		"scope", flare.Spec.EffectiveScope(),
		"job", job.Name,
		"attempt", flare.Status.Attempt,
	)
	return ctrl.Result{RequeueAfter: jobPollInterval}, nil
}

// lockHolder returns the name of another Flare of the same Astral that holds
// the run lock, or "" when it is free. It reads from the API server, not the
// cache: the Flare controller runs a single worker under leader election, so
// with a fresh read the check and the status write that follows cannot
// interleave with another Flare's.
func (r *FlareReconciler) lockHolder(ctx context.Context, flare *corev1alpha1.Flare) (string, error) {
	var list corev1alpha1.FlareList
	if err := r.apiReader().List(ctx, &list, client.InNamespace(flare.Namespace)); err != nil {
		return "", fmt.Errorf("failed to list Flares for the run lock: %w", err)
	}
	for i := range list.Items {
		other := &list.Items[i]
		if other.UID == flare.UID || other.Spec.AstralRef != flare.Spec.AstralRef {
			continue
		}
		if other.HoldsLock() {
			return other.Name, nil
		}
	}
	return "", nil
}

// ensureJob creates the attempt's Job, or returns it if a previous reconcile
// already did.
func (r *FlareReconciler) ensureJob(
	ctx context.Context,
	flare *corev1alpha1.Flare,
	astral *corev1alpha1.Astral,
	galaxy *corev1alpha1.Galaxy,
	allowedDestroy []string,
) (*batchv1.Job, error) {
	var existing batchv1.Job
	key := types.NamespacedName{Namespace: flare.Namespace, Name: JobName(flare, flare.Status.Attempt)}
	if err := r.Get(ctx, key, &existing); err == nil {
		return &existing, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to read Job %q: %w", key.Name, err)
	}

	if r.Runner.Image == "" {
		return nil, errors.New("the controller has no runner image configured (--runner-image)")
	}
	gitSecret, err := r.gitCredentials(ctx, flare, astral, galaxy)
	if err != nil {
		return nil, err
	}
	secretEnv, err := r.secretEnvNames(ctx, astral, galaxy)
	if err != nil {
		return nil, err
	}
	nonce, err := newNonce()
	if err != nil {
		return nil, err
	}

	backendConfigMap := galaxy.Status.BackendConfigMap
	job, err := buildJob(jobInputs{
		flare:            flare,
		astral:           astral,
		galaxy:           galaxy,
		request:          buildRequest(flare, astral, secretEnv, backendConfigMap != "", allowedDestroy),
		nonce:            nonce,
		gitSecret:        gitSecret,
		backendConfigMap: backendConfigMap,
	}, r.Runner)
	if err != nil {
		return nil, err
	}
	if err := controllerutil.SetControllerReference(flare, job, r.Scheme); err != nil {
		return nil, fmt.Errorf("failed to own Job: %w", err)
	}
	if err := r.Create(ctx, job); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return job, nil
		}
		return nil, fmt.Errorf("failed to create Job %q: %w", job.Name, err)
	}
	return job, nil
}

// gitCredentials returns the Secret to mount as git credentials. A static
// layout is mounted as it is. A GitHub App is exchanged here for an
// installation token that lasts an hour, written to a Secret the Flare owns:
// the App's private key never reaches a pod that runs repository code.
func (r *FlareReconciler) gitCredentials(
	ctx context.Context,
	flare *corev1alpha1.Flare,
	astral *corev1alpha1.Astral,
	galaxy *corev1alpha1.Galaxy,
) (string, error) {
	ref := astral.Spec.VCSRepository.SecretRef
	if ref == nil {
		ref = galaxy.Spec.VCSSecretRef
	}
	if ref == nil {
		return "", nil
	}

	var source corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: flare.Namespace, Name: ref.Name}, &source); err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("Secret %s not found in namespace %s", ref.Name, flare.Namespace)
		}
		return "", fmt.Errorf("failed to read VCS credentials Secret %q: %w", ref.Name, err)
	}
	if !shared.IsGitHubApp(source.Data) {
		return ref.Name, nil
	}

	token, err := shared.InstallationToken(ctx, r.HTTPClient, astral.Spec.VCSRepository.URL, source.Data)
	if err != nil {
		return "", fmt.Errorf("failed to exchange the GitHub App credentials: %w", err)
	}
	minted := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      JobName(flare, flare.Status.Attempt) + "-git",
			Namespace: flare.Namespace,
			Labels:    managedLabels(flare.Spec.UniverseRef, flare.Spec.GalaxyRef, flare.Spec.AstralRef),
		},
	}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, minted, func() error {
		minted.Labels[LabelFlare] = truncateTo(flare.Name, 63)
		minted.Type = corev1.SecretTypeOpaque
		minted.Data = map[string][]byte{
			shared.SecretKeyUsername: []byte(shared.GitHubAppUsername),
			shared.SecretKeyPassword: []byte(token),
		}
		return controllerutil.SetControllerReference(flare, minted, r.Scheme)
	})
	if err != nil {
		return "", fmt.Errorf("failed to store the installation token: %w", err)
	}
	return minted.Name, nil
}

// secretEnvNames lists the env vars the Job receives from Secrets, so the
// runner can mask their values. Only key names are read here, never values.
func (r *FlareReconciler) secretEnvNames(
	ctx context.Context,
	astral *corev1alpha1.Astral,
	galaxy *corev1alpha1.Galaxy,
) ([]string, error) {
	var names []string
	keysOf := func(name, prefix string) error {
		var secret corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{Namespace: astral.Namespace, Name: name}, &secret); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("Secret %s not found in namespace %s", name, astral.Namespace)
			}
			return fmt.Errorf("failed to read Secret %q: %w", name, err)
		}
		for key := range secret.Data {
			names = append(names, prefix+key)
		}
		return nil
	}
	for _, ref := range astral.Spec.Secrets {
		if ref.Key != "" {
			names = append(names, ref.Prefix()+ref.Key)
			continue
		}
		if err := keysOf(ref.Name, ref.Prefix()); err != nil {
			return nil, err
		}
	}
	if b := galaxy.Spec.BackendConfig; b != nil && b.SecretRef != nil {
		if err := keysOf(b.SecretRef.Name, ""); err != nil {
			return nil, err
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// observe follows a running Job: it picks up the step markers while the Job
// runs and records the result once it finishes.
func (r *FlareReconciler) observe(
	ctx context.Context,
	flare *corev1alpha1.Flare,
	astral *corev1alpha1.Astral,
	galaxy *corev1alpha1.Galaxy,
) (ctrl.Result, error) {
	if flare.Status.JobName == "" {
		// The lock was persisted but the Job never was: finish starting it.
		return r.start(ctx, flare, astral, galaxy, flare.Status.PendingDestroy)
	}

	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: flare.Namespace, Name: flare.Status.JobName}, &job); err != nil {
		if apierrors.IsNotFound(err) {
			return r.terminate(ctx, flare, corev1alpha1.FlarePhaseFailed, reasonJobLost,
				"Job "+flare.Status.JobName+" was deleted before it reported a result")
		}
		return ctrl.Result{}, fmt.Errorf("failed to read Job %q: %w", flare.Status.JobName, err)
	}

	finished, failedReason := jobFinished(&job)
	step, result := r.readLog(ctx, flare, &job)
	if step != "" && step != flare.Status.Step {
		flare.Status.Step = step
		if !finished {
			if err := r.updateStatus(ctx, flare); err != nil {
				return ctrl.Result{}, err
			}
		}
	}
	if !finished {
		return ctrl.Result{RequeueAfter: jobPollInterval}, nil
	}

	// The Job is over: the lock goes back whatever happened.
	flare.Status.LockAcquiredAt = nil

	if result == nil {
		if failedReason == batchv1.JobReasonDeadlineExceeded {
			return r.terminate(ctx, flare, corev1alpha1.FlarePhaseCancelled, reasonTimedOut,
				fmt.Sprintf("Run exceeded its %ds timeout and was stopped", int64(flare.Spec.Timeout().Seconds())))
		}
		return r.terminate(ctx, flare, corev1alpha1.FlarePhaseFailed, reasonRunFailed,
			"Job "+job.Name+" finished without reporting a result; see its log")
	}

	r.recordResult(flare, result)
	switch {
	case failedReason == batchv1.JobReasonDeadlineExceeded:
		return r.terminate(ctx, flare, corev1alpha1.FlarePhaseCancelled, reasonTimedOut,
			fmt.Sprintf("Run exceeded its %ds timeout and was stopped", int64(flare.Spec.Timeout().Seconds())))
	case result.NeedsConfirmation:
		return r.holdForConfirmation(ctx, flare, astral)
	case !result.Succeeded:
		return r.terminate(ctx, flare, corev1alpha1.FlarePhaseFailed, reasonRunFailed, result.Message)
	default:
		return r.terminate(ctx, flare, corev1alpha1.FlarePhaseSucceeded, reasonRunSucceeded, result.Message)
	}
}

// readLog extracts the last step and the result from the runner log. A log
// that cannot be read yet is not an error: the pod may still be pulling.
func (r *FlareReconciler) readLog(
	ctx context.Context,
	flare *corev1alpha1.Flare,
	job *batchv1.Job,
) (corev1alpha1.FlareStep, *runspec.Result) {
	if r.Logs == nil {
		return "", nil
	}
	text, err := r.Logs.JobLog(ctx, job.Namespace, job.Name, logTailLines)
	if err != nil {
		if !errors.Is(err, ErrNoPod) {
			log.FromContext(ctx).V(1).Info("Runner log not readable yet", "job", job.Name, "error", err.Error())
		}
		return "", nil
	}
	step, result, err := runspec.ParseLog(text, nonceFromJob(job))
	if err != nil && !errors.Is(err, runspec.ErrNoResult) {
		r.event(flare, corev1.EventTypeWarning, reasonRunFailed, "Unreadable runner result: "+err.Error())
	}
	return step, result
}

// jobFinished reports whether a Job reached a terminal condition, and the
// failure reason if it failed.
func jobFinished(job *batchv1.Job) (bool, string) {
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return true, ""
		case batchv1.JobFailed:
			return true, c.Reason
		}
	}
	return false, ""
}

// recordResult copies the runner's report onto the status.
func (r *FlareReconciler) recordResult(flare *corev1alpha1.Flare, result *runspec.Result) {
	st := &flare.Status
	st.Step = result.Step
	st.Revision = result.Revision
	st.PlanSummary = result.PlanSummary
	st.ResourceChanges = result.Changes
	st.ResourceChangesTruncated = result.ChangesTruncated
	st.UnitResults = result.Units
	st.Message = result.Message
	st.Errors = result.Errors
	st.Warnings = result.Warnings
	st.PendingDestroy = nil
	if result.NeedsConfirmation {
		st.PendingDestroy = result.PendingDestroy
	}

	output := clip(result.Output, maxStatusOutput)
	if result.Applied {
		st.ApplyOutput = output
	} else {
		st.PlanOutput = output
	}
	if result.Applied && result.PlanSummary != nil {
		s := result.PlanSummary
		st.Resources = &corev1alpha1.FlareResourceCounts{
			Created: s.ToCreate + s.ToReplace,
			Updated: s.ToUpdate,
			Deleted: s.ToDestroy + s.ToReplace,
		}
	}
}

// holdForConfirmation parks an apply whose plan destroys resources. Nothing
// was applied; the Job is over and the lock is released, so scheduled plans go
// on while a human decides.
func (r *FlareReconciler) holdForConfirmation(
	ctx context.Context,
	flare *corev1alpha1.Flare,
	astral *corev1alpha1.Astral,
) (ctrl.Result, error) {
	flare.Status.Phase = corev1alpha1.FlarePhaseAwaitingConfirmation
	message := fmt.Sprintf("Apply held: the plan destroys %d resource(s). "+
		"Set spec.confirmDestroy to %q to apply it", len(flare.Status.PendingDestroy), astral.Name)
	flare.Status.Message = message
	applyCondition(&flare.Status.Conditions, flare.Generation,
		corev1alpha1.ConditionDestroyConfirmed, metav1.ConditionFalse, reasonAwaitingConfirmation, message)
	r.event(flare, corev1.EventTypeWarning, reasonAwaitingConfirmation, message)
	if err := r.updateStatus(ctx, flare); err != nil {
		return ctrl.Result{}, err
	}
	// No requeue: the confirmation arrives as a spec update.
	return ctrl.Result{}, nil
}

// awaitConfirmation waits for spec.confirmDestroy. Once it names the Astral, a
// second Job runs with the confirmed resources as its allowlist: it plans
// again and applies only if the new plan destroys nothing beyond them.
func (r *FlareReconciler) awaitConfirmation(
	ctx context.Context,
	flare *corev1alpha1.Flare,
	astral *corev1alpha1.Astral,
	galaxy *corev1alpha1.Galaxy,
) (ctrl.Result, error) {
	if flare.Spec.Approved != nil && !*flare.Spec.Approved {
		return r.terminate(ctx, flare, corev1alpha1.FlarePhaseCancelled, reasonRejected,
			"Destroy was rejected; nothing was applied")
	}
	switch flare.Spec.ConfirmDestroy {
	case "":
		return ctrl.Result{}, nil
	case astral.Name:
	default:
		applyCondition(&flare.Status.Conditions, flare.Generation,
			corev1alpha1.ConditionDestroyConfirmed, metav1.ConditionFalse, reasonConfirmationMismatch,
			fmt.Sprintf("spec.confirmDestroy must be the Astral name %q", astral.Name))
		return ctrl.Result{}, r.updateStatus(ctx, flare)
	}

	applyCondition(&flare.Status.Conditions, flare.Generation,
		corev1alpha1.ConditionDestroyConfirmed, metav1.ConditionTrue, reasonDestroyConfirmed,
		fmt.Sprintf("Destruction of %d resource(s) confirmed", len(flare.Status.PendingDestroy)))
	r.event(flare, corev1.EventTypeNormal, reasonDestroyConfirmed, "Destroy confirmed; applying")
	return r.start(ctx, flare, astral, galaxy, flare.Status.PendingDestroy)
}

// cancel stops the Job, if any, and ends the Flare. Deleting the Job kills
// the engine mid-run; a backend state lock it held is not released, which is
// the engine's own recovery story ("force-unlock").
func (r *FlareReconciler) cancel(ctx context.Context, flare *corev1alpha1.Flare) (ctrl.Result, error) {
	if flare.Status.JobName != "" {
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: flare.Status.JobName, Namespace: flare.Namespace}}
		err := r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground))
		if err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("failed to delete Job %q: %w", job.Name, err)
		}
	}
	return r.terminate(ctx, flare, corev1alpha1.FlarePhaseCancelled, reasonCancelled, "Run was cancelled")
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
	if galaxy.Spec.Archived && flare.Status.Phase != corev1alpha1.FlarePhaseRunning {
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
	flare.Status.Message = message
	applyCondition(&flare.Status.Conditions, flare.Generation,
		corev1alpha1.ConditionApproved, metav1.ConditionFalse, reason, message)
	r.event(flare, corev1.EventTypeNormal, reason, message)

	if err := r.updateStatus(ctx, flare); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// terminate writes the final phase, releases the lock and stops the reconcile
// loop for this Flare.
func (r *FlareReconciler) terminate(
	ctx context.Context,
	flare *corev1alpha1.Flare,
	phase corev1alpha1.FlarePhase,
	reason, message string,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	flare.Status.Phase = phase
	flare.Status.Action = flare.Spec.Action
	flare.Status.LockAcquiredAt = nil
	flare.Status.Message = message
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
		"revision", flare.Status.Revision,
		"duration", flare.Status.Duration,
	)

	if err := r.updateStatus(ctx, flare); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// finalize lets a deleted Flare go. Its Job and minted Secret are owned by it
// and garbage-collected; the finalizer is only removed from Flares created by
// earlier versions, which added one to wait for an in-process run.
func (r *FlareReconciler) finalize(ctx context.Context, flare *corev1alpha1.Flare) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(flare, corev1alpha1.FlareFinalizer) {
		return ctrl.Result{}, nil
	}
	controllerutil.RemoveFinalizer(flare, corev1alpha1.FlareFinalizer)
	if err := r.Update(ctx, flare); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to remove Flare finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// mergedVariables layers the Flare overrides on top of the Astral defaults.
func mergedVariables(astral *corev1alpha1.Astral, flare *corev1alpha1.Flare) map[string]string {
	merged := make(map[string]string, len(astral.Spec.Variables)+len(flare.Spec.Variables))
	maps.Copy(merged, astral.Spec.Variables)
	maps.Copy(merged, flare.Spec.Variables)
	return merged
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
	if msg == "" || slices.Contains(list, msg) {
		return list
	}
	return append(list, msg)
}

func (r *FlareReconciler) fail(
	ctx context.Context,
	flare *corev1alpha1.Flare,
	reason string,
	cause error,
) (ctrl.Result, error) {
	if flare.Status.Phase == "" {
		flare.Status.Phase = corev1alpha1.FlarePhasePending
	}
	flare.Status.Message = cause.Error()
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

func (r *FlareReconciler) apiReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// SetupWithManager wires the Flare controller. It runs a single worker: the
// run lock relies on two Flares never being reconciled at the same time.
func (r *FlareReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.Flare{}).
		Owns(&batchv1.Job{}).
		Owns(&corev1.Secret{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Named("flare").
		Complete(r)
}
