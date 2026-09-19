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
	"maps"
	"path"
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

// DefaultGalaxyRequeue paces the periodic re-sync of a Galaxy.
const DefaultGalaxyRequeue = 5 * time.Minute

// galaxyDrainRequeue paces the wait for a Galaxy's Astrals to disappear.
const galaxyDrainRequeue = 10 * time.Second

// backendConfigKey is the entry of the generated ConfigMap holding the partial
// backend configuration passed to "init -backend-config".
const backendConfigKey = "backend.hcl"

// workspaceRoot is the container path under which every checkout is laid out as
// <root>/<universe>/<galaxy>/<astral>. The executor creates the leaf directory;
// the Galaxy owns the naming convention.
const workspaceRoot = "/var/lib/stellarcd/workspaces"

// Galaxy-specific condition reasons.
const (
	reasonBackendReady   = "BackendReady"
	reasonBackendMissing = "BackendNotConfigured"
	reasonBackendFailed  = "BackendRenderFailed"
)

// GalaxyReconciler reconciles a Galaxy: the group level where credentials, the
// engine version and the remote state backend are decided once for a team.
type GalaxyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// RequeueInterval paces periodic re-sync; zero uses DefaultGalaxyRequeue.
	RequeueInterval time.Duration
	// Now is injectable so the discovery rate limiter is testable without
	// sleeping through an interval.
	Now func() time.Time
}

// event records a Kubernetes Event against the Galaxy, when a recorder is wired.
func (r *GalaxyReconciler) event(galaxy *corev1alpha1.Galaxy, eventType, reason, message string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Event(galaxy, eventType, reason, message)
}

// +kubebuilder:rbac:groups=core.stellarcd.io,resources=galaxies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core.stellarcd.io,resources=galaxies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.stellarcd.io,resources=galaxies/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

// Reconcile resolves a Galaxy's parent Universe, verifies its VCS credentials,
// renders its state backend and republishes its census.
func (r *GalaxyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var galaxy corev1alpha1.Galaxy
	if err := r.Get(ctx, req.NamespacedName, &galaxy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !galaxy.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &galaxy)
	}

	universe, err := r.resolveUniverse(ctx, &galaxy)
	if err != nil {
		return r.fail(ctx, &galaxy, corev1alpha1.ConditionParentResolved, reasonParentMissing, err)
	}
	applyCondition(&galaxy.Status.Conditions, galaxy.Generation,
		corev1alpha1.ConditionParentResolved, metav1.ConditionTrue, reasonParentResolved,
		"Universe "+universe.Name+" resolved")

	if err := r.adopt(ctx, &galaxy, universe); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.resolveCredentials(ctx, &galaxy); err != nil {
		return r.fail(ctx, &galaxy, corev1alpha1.ConditionCredentialsResolved, reasonSecretMissing, err)
	}
	applyCondition(&galaxy.Status.Conditions, galaxy.Generation,
		corev1alpha1.ConditionCredentialsResolved, metav1.ConditionTrue, reasonSecretResolved,
		"VCS credentials are readable")

	if err := r.reconcileBackend(ctx, &galaxy); err != nil {
		return r.fail(ctx, &galaxy, corev1alpha1.ConditionBackendReady, reasonBackendFailed, err)
	}
	r.markBackend(&galaxy)

	// Discovery runs before the census so a pass that creates Astrals is
	// reflected in the counts the same reconcile, instead of a cycle later.
	if err := r.discover(ctx, &galaxy); err != nil {
		return r.fail(ctx, &galaxy, corev1alpha1.ConditionDiscovered, reasonDiscoveryFailed, err)
	}

	if err := r.census(ctx, &galaxy); err != nil {
		return r.fail(ctx, &galaxy, corev1alpha1.ConditionReconciled, reasonReconcileFailed, err)
	}

	galaxy.Status.Phase = corev1alpha1.GalaxyPhaseActive
	reason, message := reasonReconciled, "Galaxy is active"
	switch {
	case galaxy.Spec.Archived:
		galaxy.Status.Phase = corev1alpha1.GalaxyPhaseArchived
		reason, message = reasonArchived, "Galaxy is archived; no new Flare is admitted"
	case universe.Spec.Suspended:
		galaxy.Status.Phase = corev1alpha1.GalaxyPhasePending
		reason, message = reasonSuspended, "Universe "+universe.Name+" is suspended"
	}
	applyCondition(&galaxy.Status.Conditions, galaxy.Generation,
		corev1alpha1.ConditionReconciled, metav1.ConditionTrue, reason, message)

	logger.Info("🌠 Galaxy reconciled",
		"galaxy", galaxy.Name,
		"universe", galaxy.Spec.UniverseRef,
		"engine", galaxy.Spec.Terrarium.Engine,
		"phase", galaxy.Status.Phase,
		"astrals", galaxy.Status.AstralCount,
	)

	if err := r.updateStatus(ctx, &galaxy); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.requeue()}, nil
}

// resolveUniverse loads the parent Universe and refuses a Galaxy that sits
// outside its tenant namespace: allowing it would let one tenant's Galaxy
// inherit another tenant's credentials.
func (r *GalaxyReconciler) resolveUniverse(
	ctx context.Context,
	galaxy *corev1alpha1.Galaxy,
) (*corev1alpha1.Universe, error) {
	var universe corev1alpha1.Universe
	if err := r.Get(ctx, types.NamespacedName{Name: galaxy.Spec.UniverseRef}, &universe); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Universe %q does not exist", galaxy.Spec.UniverseRef)
		}
		return nil, fmt.Errorf("failed to read Universe %q: %w", galaxy.Spec.UniverseRef, err)
	}
	if universe.NamespaceName() != galaxy.Namespace {
		return nil, fmt.Errorf("Galaxy lives in namespace %q but Universe %q owns namespace %q",
			galaxy.Namespace, universe.Name, universe.NamespaceName())
	}
	return &universe, nil
}

// adopt stamps the hierarchy labels, the owner reference and the finalizer in a
// single update, so a Galaxy converges in one write instead of three.
func (r *GalaxyReconciler) adopt(
	ctx context.Context,
	galaxy *corev1alpha1.Galaxy,
	universe *corev1alpha1.Universe,
) error {
	before := galaxy.DeepCopy()

	if galaxy.Labels == nil {
		galaxy.Labels = map[string]string{}
	}
	maps.Copy(galaxy.Labels, map[string]string{corev1alpha1.LabelUniverse: universe.Name})
	controllerutil.AddFinalizer(galaxy, corev1alpha1.GalaxyFinalizer)
	if err := controllerutil.SetControllerReference(universe, galaxy, r.Scheme); err != nil {
		return fmt.Errorf("failed to set Universe ownership on Galaxy: %w", err)
	}

	if equalMeta(before, galaxy) {
		return nil
	}
	if err := r.Update(ctx, galaxy); err != nil {
		return fmt.Errorf("failed to adopt Galaxy into Universe %q: %w", universe.Name, err)
	}
	return nil
}

// resolveCredentials verifies the VCS Secret exists and carries data. Contents
// are never read into a variable that could reach a log or a status.
func (r *GalaxyReconciler) resolveCredentials(ctx context.Context, galaxy *corev1alpha1.Galaxy) error {
	if galaxy.Spec.VCSSecretRef == nil {
		return nil
	}
	return checkSecret(ctx, r.Client, galaxy.Namespace, galaxy.Spec.VCSSecretRef.Name)
}

// reconcileBackend renders the shared part of the remote state configuration
// into a ConfigMap. Only the shared part: the per-Astral state key is appended
// at run time, which is what keeps two Astrals from sharing one state file.
func (r *GalaxyReconciler) reconcileBackend(ctx context.Context, galaxy *corev1alpha1.Galaxy) error {
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: BackendConfigMapName(galaxy.Name), Namespace: galaxy.Namespace},
	}

	if galaxy.Spec.BackendConfig == nil {
		if err := r.Delete(ctx, configMap); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to remove backend ConfigMap: %w", err)
		}
		galaxy.Status.BackendConfigMap = ""
		return nil
	}

	rendered, err := RenderBackendConfig(galaxy)
	if err != nil {
		return err
	}

	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, configMap, func() error {
		configMap.Labels = managedLabels(galaxy.Spec.UniverseRef, galaxy.Name, "")
		configMap.Data = map[string]string{
			backendConfigKey: rendered,
			"workspace-root": GalaxyWorkspaceRoot(galaxy),
		}
		return controllerutil.SetControllerReference(galaxy, configMap, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("failed to reconcile backend ConfigMap: %w", err)
	}

	galaxy.Status.BackendConfigMap = configMap.Name
	return nil
}

// BackendConfigMapName is the ConfigMap holding a Galaxy's backend config.
func BackendConfigMapName(galaxy string) string {
	return galaxy + "-backend"
}

// GalaxyWorkspaceRoot is the on-disk directory under which the Astrals of this
// Galaxy are checked out.
func GalaxyWorkspaceRoot(galaxy *corev1alpha1.Galaxy) string {
	return path.Join(workspaceRoot, galaxy.Spec.UniverseRef, galaxy.Name)
}

// RenderBackendConfig produces the partial backend configuration for a Galaxy
// in HCL, ready to be passed to "init -backend-config=<file>".
func RenderBackendConfig(galaxy *corev1alpha1.Galaxy) (string, error) {
	backend := galaxy.Spec.BackendConfig
	var lines []string

	switch backend.Type {
	case corev1alpha1.BackendTypeS3:
		if backend.Bucket == "" {
			return "", fmt.Errorf("backend type %s requires spec.backendConfig.bucket", backend.Type)
		}
		lines = append(lines, hcl("bucket", backend.Bucket))
		if backend.Region != "" {
			lines = append(lines, hcl("region", backend.Region))
		}
	case corev1alpha1.BackendTypeGCS:
		if backend.Bucket == "" {
			return "", fmt.Errorf("backend type %s requires spec.backendConfig.bucket", backend.Type)
		}
		lines = append(lines, hcl("bucket", backend.Bucket))
	case corev1alpha1.BackendTypeAzureBlob:
		if backend.Container == "" || backend.StorageAccount == "" {
			return "", fmt.Errorf(
				"backend type %s requires spec.backendConfig.container and spec.backendConfig.storageAccount",
				backend.Type)
		}
		lines = append(lines, hcl("container_name", backend.Container))
		lines = append(lines, hcl("storage_account_name", backend.StorageAccount))
	default:
		return "", fmt.Errorf("unsupported backend type %q", backend.Type)
	}

	if prefix := statePrefix(galaxy); prefix != "" {
		lines = append(lines, hcl("prefix", prefix))
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// statePrefix namespaces a Galaxy's state inside a shared bucket.
func statePrefix(galaxy *corev1alpha1.Galaxy) string {
	prefix := galaxy.Spec.BackendConfig.Prefix
	scoped := path.Join(galaxy.Spec.UniverseRef, galaxy.Name)
	if prefix == "" {
		return scoped
	}
	return path.Join(prefix, scoped)
}

// hcl renders one quoted HCL assignment.
func hcl(key, value string) string {
	return fmt.Sprintf("%s = %q", key, value)
}

// markBackend records whether remote state is configured. A Galaxy without a
// backend is legal but every Astral in it keeps local state, which is reported
// rather than silently tolerated.
func (r *GalaxyReconciler) markBackend(galaxy *corev1alpha1.Galaxy) {
	if galaxy.Spec.BackendConfig == nil {
		applyCondition(&galaxy.Status.Conditions, galaxy.Generation,
			corev1alpha1.ConditionBackendReady, metav1.ConditionFalse, reasonBackendMissing,
			"No backendConfig is set; Astrals in this Galaxy keep local state")
		return
	}
	applyCondition(&galaxy.Status.Conditions, galaxy.Generation,
		corev1alpha1.ConditionBackendReady, metav1.ConditionTrue, reasonBackendReady,
		"Backend configuration rendered into ConfigMap "+galaxy.Status.BackendConfigMap)
}

// census recounts the Astrals and Flares that name this Galaxy.
func (r *GalaxyReconciler) census(ctx context.Context, galaxy *corev1alpha1.Galaxy) error {
	var astrals corev1alpha1.AstralList
	if err := r.List(ctx, &astrals,
		client.InNamespace(galaxy.Namespace),
		client.MatchingFields{IndexAstralGalaxyRef: galaxy.Name},
	); err != nil {
		return fmt.Errorf("failed to count Astrals: %w", err)
	}

	var flares corev1alpha1.FlareList
	if err := r.List(ctx, &flares,
		client.InNamespace(galaxy.Namespace),
		client.MatchingFields{IndexFlareGalaxyRef: galaxy.Name},
	); err != nil {
		return fmt.Errorf("failed to count Flares: %w", err)
	}

	galaxy.Status.AstralCount = int32(len(astrals.Items))
	galaxy.Status.FlareCount = int32(len(flares.Items))
	return nil
}

// finalize holds the Galaxy until its Astrals are gone. The Astrals are owned
// by the Galaxy so garbage collection removes them; the finalizer only makes
// the wait explicit and keeps the backend ConfigMap alive meanwhile.
func (r *GalaxyReconciler) finalize(ctx context.Context, galaxy *corev1alpha1.Galaxy) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(galaxy, corev1alpha1.GalaxyFinalizer) {
		return ctrl.Result{}, nil
	}

	var astrals corev1alpha1.AstralList
	if err := r.List(ctx, &astrals,
		client.InNamespace(galaxy.Namespace),
		client.MatchingFields{IndexAstralGalaxyRef: galaxy.Name},
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list Astrals during Galaxy cleanup: %w", err)
	}
	if len(astrals.Items) > 0 {
		logger.V(1).Info("Waiting for Astrals to be reclaimed",
			"galaxy", galaxy.Name, "remaining", len(astrals.Items))
		return ctrl.Result{RequeueAfter: galaxyDrainRequeue}, nil
	}

	controllerutil.RemoveFinalizer(galaxy, corev1alpha1.GalaxyFinalizer)
	if err := r.Update(ctx, galaxy); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to remove Galaxy finalizer: %w", err)
	}
	logger.Info("🌠 Galaxy dispersed", "galaxy", galaxy.Name)
	return ctrl.Result{}, nil
}

func (r *GalaxyReconciler) fail(
	ctx context.Context,
	galaxy *corev1alpha1.Galaxy,
	condType, reason string,
	cause error,
) (ctrl.Result, error) {
	galaxy.Status.Phase = corev1alpha1.GalaxyPhasePending
	applyCondition(&galaxy.Status.Conditions, galaxy.Generation,
		condType, metav1.ConditionFalse, reason, cause.Error())
	applyCondition(&galaxy.Status.Conditions, galaxy.Generation,
		corev1alpha1.ConditionReconciled, metav1.ConditionFalse, reasonReconcileFailed, cause.Error())
	if r.Recorder != nil {
		r.Recorder.Event(galaxy, corev1.EventTypeWarning, reason, cause.Error())
	}

	if err := r.updateStatus(ctx, galaxy); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, cause
}

func (r *GalaxyReconciler) updateStatus(ctx context.Context, galaxy *corev1alpha1.Galaxy) error {
	galaxy.Status.ObservedGeneration = galaxy.Generation
	galaxy.Status.LastSync = ptrTime(metav1.Now())
	if err := r.Status().Update(ctx, galaxy); err != nil {
		return fmt.Errorf("failed to update Galaxy status: %w", err)
	}
	return nil
}

func (r *GalaxyReconciler) requeue() time.Duration {
	if r.RequeueInterval > 0 {
		return r.RequeueInterval
	}
	return DefaultGalaxyRequeue
}

// SetupWithManager wires the Galaxy controller.
func (r *GalaxyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.Galaxy{}).
		Owns(&corev1.ConfigMap{}).
		Watches(&corev1alpha1.Astral{}, handler.EnqueueRequestsFromMapFunc(mapAstralToGalaxy)).
		Named("galaxy").
		Complete(r)
}

// mapAstralToGalaxy routes an Astral back to the Galaxy named in its spec.
func mapAstralToGalaxy(_ context.Context, obj client.Object) []reconcile.Request {
	astral, ok := obj.(*corev1alpha1.Astral)
	if !ok || astral.Spec.GalaxyRef == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Namespace: astral.Namespace, Name: astral.Spec.GalaxyRef},
	}}
}

// checkSecret verifies a Secret exists and is non-empty without ever holding
// its values.
func checkSecret(ctx context.Context, reader client.Reader, namespace, name string) error {
	var secret corev1.Secret
	if err := reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("Secret %q not found in namespace %q", name, namespace)
		}
		return fmt.Errorf("failed to read Secret %q: %w", name, err)
	}
	if len(secret.Data) == 0 {
		return fmt.Errorf("Secret %q is empty", name)
	}
	return nil
}

// equalMeta reports whether two revisions of an object carry the same labels,
// finalizers and owner references, i.e. whether an Update would be a no-op.
func equalMeta(before, after client.Object) bool {
	return maps.Equal(before.GetLabels(), after.GetLabels()) &&
		slices.Equal(before.GetFinalizers(), after.GetFinalizers()) &&
		len(before.GetOwnerReferences()) == len(after.GetOwnerReferences())
}
