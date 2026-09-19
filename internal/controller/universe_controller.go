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
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
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

// Default names of the ClusterRoles bound into every tenant namespace. They are
// shipped by config/rbac and can be overridden on the reconciler.
const (
	DefaultTenantAdminClusterRole  = "stellarcd-tenant-admin"
	DefaultTenantViewerClusterRole = "stellarcd-tenant-viewer"
)

// RoleBinding names created inside a tenant namespace.
const (
	tenantAdminBinding  = "stellarcd-admins"
	tenantViewerBinding = "stellarcd-viewers"
)

// tenantQuotaName is the ResourceQuota materialising spec.defaultQuota.
const tenantQuotaName = "stellarcd-tenant-quota"

// DefaultUniverseRequeue paces the drift check between spec and cluster state.
const DefaultUniverseRequeue = 5 * time.Minute

// namespaceDrainRequeue paces the wait for a tenant namespace to disappear.
const namespaceDrainRequeue = 5 * time.Second

// Universe-specific condition reasons.
const (
	reasonNamespaceReady   = "NamespaceReady"
	reasonNamespacePending = "NamespacePending"
	reasonQuotaEnforced    = "QuotaEnforced"
	reasonQuotaUnbounded   = "QuotaUnbounded"
	reasonPoliciesApplied  = "PoliciesApplied"
)

// UniverseReconciler reconciles a Universe: it owns the tenant namespace and
// everything that isolates it from its neighbours.
type UniverseReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// TenantAdminClusterRole is bound to spec.rbac.adminUsers in the tenant
	// namespace. Empty falls back to DefaultTenantAdminClusterRole.
	TenantAdminClusterRole string
	// TenantViewerClusterRole is bound to spec.rbac.viewerUsers.
	TenantViewerClusterRole string
	// RequeueInterval paces periodic re-sync; zero uses DefaultUniverseRequeue.
	RequeueInterval time.Duration
}

// +kubebuilder:rbac:groups=core.stellarcd.io,resources=universes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core.stellarcd.io,resources=universes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.stellarcd.io,resources=universes/finalizers,verbs=update
// +kubebuilder:rbac:groups=core.stellarcd.io,resources=galaxies;astrals;flares,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=resourcequotas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=bind,resourceNames=stellarcd-tenant-admin;stellarcd-tenant-viewer

// Reconcile brings a Universe's isolation namespace, quota, network policies
// and RBAC in line with its spec, then republishes the tenant census.
func (r *UniverseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var universe corev1alpha1.Universe
	if err := r.Get(ctx, req.NamespacedName, &universe); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !universe.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &universe)
	}

	if !controllerutil.ContainsFinalizer(&universe, corev1alpha1.UniverseFinalizer) {
		controllerutil.AddFinalizer(&universe, corev1alpha1.UniverseFinalizer)
		if err := r.Update(ctx, &universe); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add Universe finalizer: %w", err)
		}
		// The update bumped resourceVersion; the watch requeues us immediately.
		return ctrl.Result{}, nil
	}

	universe.Status.Namespace = universe.NamespaceName()

	if err := r.reconcileNamespace(ctx, &universe); err != nil {
		return r.fail(ctx, &universe, corev1alpha1.ConditionNamespaceReady, reasonNamespacePending, err)
	}
	applyCondition(&universe.Status.Conditions, universe.Generation,
		corev1alpha1.ConditionNamespaceReady, metav1.ConditionTrue, reasonNamespaceReady,
		"Tenant namespace "+universe.NamespaceName()+" is ready")

	if err := r.reconcileQuota(ctx, &universe); err != nil {
		return r.fail(ctx, &universe, corev1alpha1.ConditionQuotaEnforced, reasonReconcileFailed, err)
	}
	r.markQuota(&universe)

	if err := r.reconcileNetworkPolicies(ctx, &universe); err != nil {
		return r.fail(ctx, &universe, corev1alpha1.ConditionPoliciesApplied, reasonReconcileFailed, err)
	}
	if err := r.reconcileRBAC(ctx, &universe); err != nil {
		return r.fail(ctx, &universe, corev1alpha1.ConditionPoliciesApplied, reasonReconcileFailed, err)
	}
	applyCondition(&universe.Status.Conditions, universe.Generation,
		corev1alpha1.ConditionPoliciesApplied, metav1.ConditionTrue, reasonPoliciesApplied,
		"Network policies and role bindings are in sync")

	if err := r.census(ctx, &universe); err != nil {
		return r.fail(ctx, &universe, corev1alpha1.ConditionReconciled, reasonReconcileFailed, err)
	}

	universe.Status.Phase = corev1alpha1.UniversePhaseActive
	reason, message := reasonReconciled, "Universe is active"
	if universe.Spec.Suspended {
		universe.Status.Phase = corev1alpha1.UniversePhaseSuspended
		reason, message = reasonSuspended, "Universe is suspended; no Flare will start"
	}
	applyCondition(&universe.Status.Conditions, universe.Generation,
		corev1alpha1.ConditionReconciled, metav1.ConditionTrue, reason, message)

	logger.Info("🌌 Universe reconciled",
		"universe", universe.Name,
		"namespace", universe.Status.Namespace,
		"phase", universe.Status.Phase,
		"galaxies", universe.Status.GalaxyCount,
		"astrals", universe.Status.AstralCount,
		"flares", universe.Status.FlareCount,
	)

	if err := r.updateStatus(ctx, &universe); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.requeue()}, nil
}

// finalize tears the tenant down: the namespace goes first, and the finalizer
// is only dropped once Kubernetes confirms it is gone. Removing it earlier
// would let the Universe disappear while Galaxies are still terminating.
func (r *UniverseReconciler) finalize(ctx context.Context, universe *corev1alpha1.Universe) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(universe, corev1alpha1.UniverseFinalizer) {
		return ctrl.Result{}, nil
	}

	universe.Status.Phase = corev1alpha1.UniversePhaseDeleting
	applyCondition(&universe.Status.Conditions, universe.Generation,
		corev1alpha1.ConditionReconciled, metav1.ConditionFalse, reasonDeleting, "Universe is being deleted")
	if err := r.updateStatus(ctx, universe); err != nil && !apierrors.IsConflict(err) {
		return ctrl.Result{}, err
	}

	var namespace corev1.Namespace
	err := r.Get(ctx, types.NamespacedName{Name: universe.NamespaceName()}, &namespace)
	switch {
	case apierrors.IsNotFound(err):
		// Nothing left to drain.
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("failed to read tenant namespace: %w", err)
	case namespace.DeletionTimestamp.IsZero():
		if delErr := r.Delete(ctx, &namespace); delErr != nil && !apierrors.IsNotFound(delErr) {
			return ctrl.Result{}, fmt.Errorf("failed to delete tenant namespace: %w", delErr)
		}
		r.event(universe, corev1.EventTypeNormal, reasonDeleting,
			"Deleting tenant namespace "+universe.NamespaceName())
		return ctrl.Result{RequeueAfter: namespaceDrainRequeue}, nil
	default:
		logger.V(1).Info("Waiting for tenant namespace to drain", "namespace", namespace.Name)
		return ctrl.Result{RequeueAfter: namespaceDrainRequeue}, nil
	}

	controllerutil.RemoveFinalizer(universe, corev1alpha1.UniverseFinalizer)
	if err := r.Update(ctx, universe); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to remove Universe finalizer: %w", err)
	}
	logger.Info("🌌 Universe collapsed", "universe", universe.Name)
	return ctrl.Result{}, nil
}

// reconcileNamespace creates or updates the tenant namespace, propagating the
// spec labels and annotations without clobbering foreign ones.
func (r *UniverseReconciler) reconcileNamespace(ctx context.Context, universe *corev1alpha1.Universe) error {
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: universe.NamespaceName()}}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, namespace, func() error {
		if namespace.Labels == nil {
			namespace.Labels = map[string]string{}
		}
		maps.Copy(namespace.Labels, universe.Spec.Labels)
		namespace.Labels[corev1alpha1.LabelUniverse] = universe.Name
		namespace.Labels[corev1alpha1.LabelManagedBy] = corev1alpha1.ManagedByValue

		if len(universe.Spec.Annotations) > 0 {
			if namespace.Annotations == nil {
				namespace.Annotations = map[string]string{}
			}
			maps.Copy(namespace.Annotations, universe.Spec.Annotations)
		}
		return controllerutil.SetControllerReference(universe, namespace, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("failed to reconcile tenant namespace %q: %w", namespace.Name, err)
	}
	if op == controllerutil.OperationResultCreated {
		r.event(universe, corev1.EventTypeNormal, reasonNamespaceReady,
			"Created tenant namespace "+namespace.Name)
	}
	return nil
}

// reconcileQuota materialises spec.defaultQuota, deleting the ResourceQuota
// when the spec stops constraining anything.
func (r *UniverseReconciler) reconcileQuota(ctx context.Context, universe *corev1alpha1.Universe) error {
	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: tenantQuotaName, Namespace: universe.NamespaceName()},
	}

	if universe.Spec.DefaultQuota.IsEmpty() {
		if err := r.Delete(ctx, quota); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to remove tenant quota: %w", err)
		}
		return nil
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, quota, func() error {
		quota.Labels = managedLabels(universe.Name, "", "")
		quota.Spec.Hard = quotaResourceList(universe.Spec.DefaultQuota)
		return controllerutil.SetControllerReference(universe, quota, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("failed to reconcile tenant quota: %w", err)
	}
	return nil
}

// quotaResourceList translates the CRD quota onto the upstream resource names.
func quotaResourceList(quota *corev1alpha1.UniverseQuota) corev1.ResourceList {
	hard := corev1.ResourceList{}
	if quota.CPU != nil {
		hard[corev1.ResourceLimitsCPU] = *quota.CPU
	}
	if quota.Memory != nil {
		hard[corev1.ResourceLimitsMemory] = *quota.Memory
	}
	if quota.Storage != nil {
		hard[corev1.ResourceRequestsStorage] = *quota.Storage
	}
	return hard
}

// markQuota records whether the tenant is bounded.
func (r *UniverseReconciler) markQuota(universe *corev1alpha1.Universe) {
	if universe.Spec.DefaultQuota.IsEmpty() {
		applyCondition(&universe.Status.Conditions, universe.Generation,
			corev1alpha1.ConditionQuotaEnforced, metav1.ConditionFalse, reasonQuotaUnbounded,
			"No defaultQuota is set; the tenant namespace is unbounded")
		return
	}
	applyCondition(&universe.Status.Conditions, universe.Generation,
		corev1alpha1.ConditionQuotaEnforced, metav1.ConditionTrue, reasonQuotaEnforced,
		"ResourceQuota "+tenantQuotaName+" matches spec.defaultQuota")
}

// reconcileNetworkPolicies syncs the declared policies and prunes the managed
// ones that dropped out of the spec. Policies stellarCD does not own are left
// alone so cluster-wide baselines survive.
func (r *UniverseReconciler) reconcileNetworkPolicies(ctx context.Context, universe *corev1alpha1.Universe) error {
	desired := make(map[string]struct{}, len(universe.Spec.NetworkPolicies))

	for i := range universe.Spec.NetworkPolicies {
		declared := &universe.Spec.NetworkPolicies[i]
		desired[declared.Name] = struct{}{}

		policy := &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: declared.Name, Namespace: universe.NamespaceName()},
		}
		_, err := controllerutil.CreateOrUpdate(ctx, r.Client, policy, func() error {
			policy.Labels = managedLabels(universe.Name, "", "")
			declared.Spec.DeepCopyInto(&policy.Spec)
			return controllerutil.SetControllerReference(universe, policy, r.Scheme)
		})
		if err != nil {
			return fmt.Errorf("failed to reconcile NetworkPolicy %q: %w", declared.Name, err)
		}
	}

	var existing networkingv1.NetworkPolicyList
	if err := r.List(ctx, &existing,
		client.InNamespace(universe.NamespaceName()),
		client.MatchingLabels{corev1alpha1.LabelManagedBy: corev1alpha1.ManagedByValue},
	); err != nil {
		return fmt.Errorf("failed to list tenant NetworkPolicies: %w", err)
	}
	for i := range existing.Items {
		policy := &existing.Items[i]
		if _, keep := desired[policy.Name]; keep {
			continue
		}
		if err := r.Delete(ctx, policy); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to prune NetworkPolicy %q: %w", policy.Name, err)
		}
	}
	return nil
}

// reconcileRBAC binds the declared identities to the tenant ClusterRoles inside
// the tenant namespace. An empty user list removes the binding entirely rather
// than leaving an empty one behind.
func (r *UniverseReconciler) reconcileRBAC(ctx context.Context, universe *corev1alpha1.Universe) error {
	bindings := []struct {
		name        string
		clusterRole string
		users       []string
	}{
		{tenantAdminBinding, r.adminClusterRole(), nil},
		{tenantViewerBinding, r.viewerClusterRole(), nil},
	}
	if universe.Spec.RBAC != nil {
		bindings[0].users = universe.Spec.RBAC.AdminUsers
		bindings[1].users = universe.Spec.RBAC.ViewerUsers
	}

	for _, desired := range bindings {
		binding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: desired.name, Namespace: universe.NamespaceName()},
		}

		if len(desired.users) == 0 {
			if err := r.Delete(ctx, binding); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to remove RoleBinding %q: %w", desired.name, err)
			}
			continue
		}

		_, err := controllerutil.CreateOrUpdate(ctx, r.Client, binding, func() error {
			binding.Labels = managedLabels(universe.Name, "", "")
			// RoleRef is immutable; CreateOrUpdate would fail on a changed role
			// name, which is the correct signal that the binding must be recreated.
			binding.RoleRef = rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     desired.clusterRole,
			}
			binding.Subjects = userSubjects(desired.users)
			return controllerutil.SetControllerReference(universe, binding, r.Scheme)
		})
		if err != nil {
			return fmt.Errorf("failed to reconcile RoleBinding %q: %w", desired.name, err)
		}
	}
	return nil
}

// userSubjects renders user names as RBAC subjects.
func userSubjects(users []string) []rbacv1.Subject {
	subjects := make([]rbacv1.Subject, 0, len(users))
	for _, user := range users {
		subjects = append(subjects, rbacv1.Subject{
			APIGroup: rbacv1.GroupName,
			Kind:     rbacv1.UserKind,
			Name:     user,
		})
	}
	return subjects
}

// census recounts the tenant population. Everything inside a tenant namespace
// belongs to the Universe that owns it, so a namespace-scoped list is exact.
func (r *UniverseReconciler) census(ctx context.Context, universe *corev1alpha1.Universe) error {
	inNamespace := client.InNamespace(universe.NamespaceName())

	var galaxies corev1alpha1.GalaxyList
	if err := r.List(ctx, &galaxies, inNamespace); err != nil {
		return fmt.Errorf("failed to count Galaxies: %w", err)
	}
	var astrals corev1alpha1.AstralList
	if err := r.List(ctx, &astrals, inNamespace); err != nil {
		return fmt.Errorf("failed to count Astrals: %w", err)
	}
	var flares corev1alpha1.FlareList
	if err := r.List(ctx, &flares, inNamespace); err != nil {
		return fmt.Errorf("failed to count Flares: %w", err)
	}

	universe.Status.GalaxyCount = int32(len(galaxies.Items))
	universe.Status.AstralCount = int32(len(astrals.Items))
	universe.Status.FlareCount = int32(len(flares.Items))
	return nil
}

// fail records a failed step and hands the retry to controller-runtime.
func (r *UniverseReconciler) fail(
	ctx context.Context,
	universe *corev1alpha1.Universe,
	condType, reason string,
	cause error,
) (ctrl.Result, error) {
	universe.Status.Phase = corev1alpha1.UniversePhasePending
	applyCondition(&universe.Status.Conditions, universe.Generation,
		condType, metav1.ConditionFalse, reason, cause.Error())
	applyCondition(&universe.Status.Conditions, universe.Generation,
		corev1alpha1.ConditionReconciled, metav1.ConditionFalse, reasonReconcileFailed, cause.Error())
	r.event(universe, corev1.EventTypeWarning, reason, cause.Error())

	if err := r.updateStatus(ctx, universe); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, cause
}

// updateStatus stamps the observed generation and writes the status subresource.
func (r *UniverseReconciler) updateStatus(ctx context.Context, universe *corev1alpha1.Universe) error {
	universe.Status.ObservedGeneration = universe.Generation
	universe.Status.LastUpdated = ptrTime(metav1.Now())
	if err := r.Status().Update(ctx, universe); err != nil {
		return fmt.Errorf("failed to update Universe status: %w", err)
	}
	return nil
}

func (r *UniverseReconciler) event(universe *corev1alpha1.Universe, eventType, reason, message string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Event(universe, eventType, reason, message)
}

func (r *UniverseReconciler) adminClusterRole() string {
	if r.TenantAdminClusterRole != "" {
		return r.TenantAdminClusterRole
	}
	return DefaultTenantAdminClusterRole
}

func (r *UniverseReconciler) viewerClusterRole() string {
	if r.TenantViewerClusterRole != "" {
		return r.TenantViewerClusterRole
	}
	return DefaultTenantViewerClusterRole
}

func (r *UniverseReconciler) requeue() time.Duration {
	if r.RequeueInterval > 0 {
		return r.RequeueInterval
	}
	return DefaultUniverseRequeue
}

// SetupWithManager wires the Universe controller. It watches the objects it
// owns so a hand-edited NetworkPolicy is reverted, and watches Galaxies so the
// tenant census stays fresh without polling.
func (r *UniverseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.Universe{}).
		Owns(&corev1.Namespace{}).
		Owns(&corev1.ResourceQuota{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Owns(&rbacv1.RoleBinding{}).
		Watches(&corev1alpha1.Galaxy{}, handler.EnqueueRequestsFromMapFunc(mapToUniverse)).
		Named("universe").
		Complete(r)
}

// mapToUniverse routes a child object back to the Universe named in its spec.
func mapToUniverse(_ context.Context, obj client.Object) []reconcile.Request {
	galaxy, ok := obj.(*corev1alpha1.Galaxy)
	if !ok || galaxy.Spec.UniverseRef == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: galaxy.Spec.UniverseRef}}}
}

// managedLabels is the label set stamped on every object stellarCD owns.
func managedLabels(universe, galaxy, astral string) map[string]string {
	labels := map[string]string{corev1alpha1.LabelManagedBy: corev1alpha1.ManagedByValue}
	if universe != "" {
		labels[corev1alpha1.LabelUniverse] = universe
	}
	if galaxy != "" {
		labels[corev1alpha1.LabelGalaxy] = galaxy
	}
	if astral != "" {
		labels[corev1alpha1.LabelAstral] = astral
	}
	return labels
}

// ptrTime returns a pointer to t; metav1.Time has no helper of its own.
func ptrTime(t metav1.Time) *metav1.Time { return &t }
