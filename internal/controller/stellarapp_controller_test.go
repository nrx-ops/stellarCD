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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
)

var _ = Describe("StellarApp Controller", func() {
	const namespace = "default"

	var (
		reconciler *StellarAppReconciler
		recorder   *record.FakeRecorder
	)

	BeforeEach(func() {
		recorder = record.NewFakeRecorder(10)
		reconciler = &StellarAppReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: recorder,
		}
	})

	// newApp builds a StellarApp with only the required fields set, leaving the
	// rest to CRD defaulting.
	newApp := func(name string, secretRef *corev1alpha1.SecretRef) *corev1alpha1.StellarApp {
		return &corev1alpha1.StellarApp{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: corev1alpha1.StellarAppSpec{
				GitRepository: corev1alpha1.GitRepositorySpec{
					URL:       "https://github.com/nrx-ops/stellarcd-examples.git",
					SecretRef: secretRef,
				},
				TerraformPath: "environments/dev",
			},
		}
	}

	// createApp persists app and registers its cleanup.
	createApp := func(app *corev1alpha1.StellarApp) types.NamespacedName {
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		DeferCleanup(func() {
			err := k8sClient.Delete(ctx, app)
			if err != nil && !apierrors.IsNotFound(err) {
				Expect(err).NotTo(HaveOccurred())
			}
		})
		return client.ObjectKeyFromObject(app)
	}

	// fetch reloads the object from the API server.
	fetch := func(key types.NamespacedName) *corev1alpha1.StellarApp {
		var out corev1alpha1.StellarApp
		Expect(k8sClient.Get(ctx, key, &out)).To(Succeed())
		return &out
	}

	Context("when the spec is valid and needs no credentials", func() {
		It("records the observed generation and requeues at the configured interval", func() {
			key := createApp(newApp("valid-no-secret", nil))

			result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(DefaultReconcileInterval))

			app := fetch(key)
			Expect(app.Status.Phase).To(Equal(corev1alpha1.PhaseSyncing))
			Expect(app.Status.LastError).To(BeEmpty())
			Expect(app.Status.ObservedGeneration).To(Equal(app.Generation))

			// No sync has actually run, so neither condition may report success.
			ready := meta.FindStatusCondition(app.Status.Conditions, corev1alpha1.ConditionReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			Expect(ready.Reason).To(Equal(reasonExecutorPending))

			synced := meta.FindStatusCondition(app.Status.Conditions, corev1alpha1.ConditionSynced)
			Expect(synced).NotTo(BeNil())
			Expect(synced.Status).To(Equal(metav1.ConditionFalse))
		})

		It("honours an explicit interval", func() {
			app := newApp("valid-custom-interval", nil)
			app.Spec.Interval = &metav1.Duration{Duration: 90 * time.Second}
			key := createApp(app)

			result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(90 * time.Second))
		})
	})

	Context("when the referenced credentials Secret is missing", func() {
		It("marks the app Degraded and returns the error for backoff", func() {
			key := createApp(newApp("missing-secret", &corev1alpha1.SecretRef{Name: "absent"}))

			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))

			app := fetch(key)
			Expect(app.Status.Phase).To(Equal(corev1alpha1.PhaseDegraded))
			Expect(app.Status.LastError).To(ContainSubstring("absent"))

			ready := meta.FindStatusCondition(app.Status.Conditions, corev1alpha1.ConditionReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			Expect(ready.Reason).To(Equal(reasonSecretNotFound))

			Expect(recorder.Events).To(Receive(ContainSubstring(reasonSecretNotFound)))
		})
	})

	Context("when the referenced credentials Secret exists", func() {
		It("accepts the spec", func() {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "git-creds", Namespace: namespace},
				Data:       map[string][]byte{"token": []byte("s3cr3t")},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			DeferCleanup(func() { Expect(k8sClient.Delete(ctx, secret)).To(Succeed()) })

			key := createApp(newApp("present-secret", &corev1alpha1.SecretRef{Name: "git-creds"}))

			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(fetch(key).Status.Phase).To(Equal(corev1alpha1.PhaseSyncing))
		})

		It("rejects an empty Secret", func() {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "empty-creds", Namespace: namespace},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			DeferCleanup(func() { Expect(k8sClient.Delete(ctx, secret)).To(Succeed()) })

			key := createApp(newApp("empty-secret", &corev1alpha1.SecretRef{Name: "empty-creds"}))

			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
			Expect(err).To(MatchError(ContainSubstring("is empty")))
			Expect(fetch(key).Status.Phase).To(Equal(corev1alpha1.PhaseDegraded))
		})
	})

	Context("when the StellarApp no longer exists", func() {
		It("returns without an error", func() {
			key := types.NamespacedName{Name: "gone", Namespace: namespace}
			result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.IsZero()).To(BeTrue())
		})
	})
})

var _ = Describe("StellarApp CRD defaulting", func() {
	It("applies the schema defaults on create", func() {
		app := &corev1alpha1.StellarApp{
			ObjectMeta: metav1.ObjectMeta{Name: "defaulted", Namespace: "default"},
			Spec: corev1alpha1.StellarAppSpec{
				GitRepository: corev1alpha1.GitRepositorySpec{URL: "https://example.com/repo.git"},
				TerraformPath: "live/prod",
			},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) })

		var out corev1alpha1.StellarApp
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(app), &out)).To(Succeed())
		Expect(out.Spec.GitRepository.Ref).To(Equal("main"))
		Expect(out.Spec.Executor).To(Equal(corev1alpha1.ExecutorTypeTerraform))
		Expect(out.Spec.Interval).NotTo(BeNil())
		Expect(out.Spec.Interval.Duration).To(Equal(5 * time.Minute))
	})

	It("rejects a spec without a Git URL", func() {
		app := &corev1alpha1.StellarApp{
			ObjectMeta: metav1.ObjectMeta{Name: "no-url", Namespace: "default"},
			Spec:       corev1alpha1.StellarAppSpec{TerraformPath: "live/prod"},
		}
		Expect(k8sClient.Create(ctx, app)).NotTo(Succeed())
	})
})
