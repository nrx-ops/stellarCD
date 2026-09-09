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
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
)

// DefaultReconcileInterval is used when spec.interval is unset. The CRD carries
// the same default, so this only applies to objects that bypassed defaulting.
const DefaultReconcileInterval = 5 * time.Minute

// Condition reasons reported on StellarApp status.
const (
	reasonSecretNotFound   = "SecretNotFound"
	reasonSecretUnreadable = "SecretUnreadable"
	reasonSpecValid        = "SpecValid"
	reasonExecutorPending  = "ExecutorPending"
)

// StellarAppReconciler reconciles a StellarApp object.
type StellarAppReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=core.stellarcd.io,resources=stellarapps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core.stellarcd.io,resources=stellarapps/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.stellarcd.io,resources=stellarapps/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives a StellarApp towards its desired state and keeps the status
// subresource in step with the observed spec generation.
func (r *StellarAppReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var app corev1alpha1.StellarApp
	if err := r.Get(ctx, req.NamespacedName, &app); err != nil {
		// A deleted StellarApp needs no cleanup yet: nothing outside the cluster
		// is owned until the Terraform executor manages remote state.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !app.DeletionTimestamp.IsZero() {
		logger.V(1).Info("StellarApp is being deleted, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	interval := app.Spec.ReconcileInterval(metav1.Duration{Duration: DefaultReconcileInterval})

	logger.Info("Reconciling StellarApp",
		"gitRepository", app.Spec.GitRepository.URL,
		"ref", app.Spec.GitRepository.Ref,
		"terraformPath", app.Spec.TerraformPath,
		"executor", app.Spec.Executor,
		"autoApply", app.Spec.AutoApply,
		"interval", interval.Duration.String(),
	)

	// Resolving the credentials Secret is the one prerequisite the controller can
	// verify without touching Git or Terraform. Returning the error surfaces it on
	// the status and hands the retry to controller-runtime's exponential backoff.
	if err := r.resolveGitCredentials(ctx, &app); err != nil {
		r.Recorder.Event(&app, corev1.EventTypeWarning, reasonSecretNotFound, err.Error())
		return r.markDegraded(ctx, &app, reasonSecretNotFound, err)
	}

	// TODO(nrx-ops): run the GitOps workflow here once the executor lands:
	// git fetch -> terraform plan -> terraform apply, serialized per
	// (repository, terraformPath) so concurrent runs cannot corrupt state.
	// Until then the controller only records that it observed this generation,
	// so the status never claims a sync that did not happen.
	return r.markPending(ctx, &app, interval)
}

// resolveGitCredentials verifies that the referenced Secret exists and is
// readable. It deliberately does not log or return Secret contents.
func (r *StellarAppReconciler) resolveGitCredentials(ctx context.Context, app *corev1alpha1.StellarApp) error {
	ref := app.Spec.GitRepository.SecretRef
	if ref == nil {
		return nil
	}

	var secret corev1.Secret
	key := types.NamespacedName{Namespace: app.Namespace, Name: ref.Name}
	if err := r.Get(ctx, key, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("git credentials Secret %q not found in namespace %q", ref.Name, app.Namespace)
		}
		return fmt.Errorf("failed to read git credentials Secret %q: %w", ref.Name, err)
	}

	if len(secret.Data) == 0 {
		return fmt.Errorf("git credentials Secret %q is empty", ref.Name)
	}

	return nil
}

// markDegraded records a reconciliation failure and lets controller-runtime
// apply its backoff by returning the underlying error.
func (r *StellarAppReconciler) markDegraded(
	ctx context.Context,
	app *corev1alpha1.StellarApp,
	reason string,
	cause error,
) (ctrl.Result, error) {
	app.Status.Phase = corev1alpha1.PhaseDegraded
	app.Status.LastError = cause.Error()
	setCondition(app, corev1alpha1.ConditionReady, metav1.ConditionFalse, reason, cause.Error())
	setCondition(app, corev1alpha1.ConditionSynced, metav1.ConditionFalse, reason, cause.Error())

	if err := r.updateStatus(ctx, app); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, cause
}

// markPending records that the spec was observed and validated but that no sync
// has been performed, then schedules the next pass.
func (r *StellarAppReconciler) markPending(
	ctx context.Context,
	app *corev1alpha1.StellarApp,
	interval metav1.Duration,
) (ctrl.Result, error) {
	const msg = "Spec validated; the Terraform executor is not wired up yet so no plan or apply has run"

	app.Status.Phase = corev1alpha1.PhaseSyncing
	app.Status.LastError = ""
	setCondition(app, corev1alpha1.ConditionSynced, metav1.ConditionFalse, reasonSpecValid, msg)
	setCondition(app, corev1alpha1.ConditionReady, metav1.ConditionFalse, reasonExecutorPending, msg)

	if err := r.updateStatus(ctx, app); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: interval.Duration}, nil
}

// updateStatus stamps the observed generation and writes the status subresource.
func (r *StellarAppReconciler) updateStatus(ctx context.Context, app *corev1alpha1.StellarApp) error {
	app.Status.ObservedGeneration = app.Generation
	if err := r.Status().Update(ctx, app); err != nil {
		// A conflict means someone else wrote first; requeueing re-reads the object.
		return fmt.Errorf("failed to update StellarApp status: %w", err)
	}
	return nil
}

// setCondition upserts a condition, preserving LastTransitionTime when the
// status has not actually changed.
func setCondition(app *corev1alpha1.StellarApp, condType string, status metav1.ConditionStatus, reason, msg string) {
	for i := range app.Status.Conditions {
		existing := &app.Status.Conditions[i]
		if existing.Type != condType {
			continue
		}
		if existing.Status != status {
			existing.LastTransitionTime = metav1.Now()
		}
		existing.Status = status
		existing.Reason = reason
		existing.Message = msg
		existing.ObservedGeneration = app.Generation
		return
	}

	app.Status.Conditions = append(app.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: app.Generation,
		LastTransitionTime: metav1.Now(),
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *StellarAppReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.StellarApp{}).
		Named("stellarapp").
		Complete(r)
}
