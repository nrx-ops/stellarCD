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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
	"github.com/nrx-ops/stellarCD/internal/controller/shared"
	"github.com/nrx-ops/stellarCD/internal/runspec"
)

// fakeLogs stands in for the kubelet: envtest runs no pods, so the tests write
// the runner's log themselves.
type fakeLogs struct {
	mu   sync.Mutex
	logs map[string]string
}

func (f *fakeLogs) JobLog(_ context.Context, _, job string, _ int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	log, ok := f.logs[job]
	if !ok {
		return "", ErrNoPod
	}
	return log, nil
}

func (f *fakeLogs) set(job, log string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.logs == nil {
		f.logs = map[string]string{}
	}
	f.logs[job] = log
}

var _ = Describe("Flare controller", func() {
	var (
		ns         string
		logs       *fakeLogs
		reconciler *FlareReconciler
	)

	newNamespace := func() string {
		n := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "flare-"}}
		Expect(k8sClient.Create(ctx, n)).To(Succeed())
		return n.Name
	}

	makeGalaxy := func(name string, mutate func(*corev1alpha1.Galaxy)) {
		g := &corev1alpha1.Galaxy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: corev1alpha1.GalaxySpec{
				UniverseRef:    "u",
				ApprovalPolicy: corev1alpha1.ApprovalPolicyAuto,
			},
		}
		if mutate != nil {
			mutate(g)
		}
		Expect(k8sClient.Create(ctx, g)).To(Succeed())
	}

	makeAstral := func(name string, mutate func(*corev1alpha1.Astral)) *corev1alpha1.Astral {
		a := &corev1alpha1.Astral{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: corev1alpha1.AstralSpec{
				UniverseRef: "u",
				GalaxyRef:   "g",
				VCSRepository: corev1alpha1.VCSRepository{
					URL: "https://github.com/4sh/cinng-iac", Branch: "main", Path: ".",
				},
			},
		}
		if mutate != nil {
			mutate(a)
		}
		Expect(k8sClient.Create(ctx, a)).To(Succeed())
		// The Flare controller only runs against an Astral that validated.
		applyCondition(&a.Status.Conditions, a.Generation, corev1alpha1.ConditionConfigValid,
			metav1.ConditionTrue, reasonConfigValid, "ok")
		Expect(k8sClient.Status().Update(ctx, a)).To(Succeed())
		return a
	}

	makeFlare := func(name string, action corev1alpha1.FlareAction, mutate func(*corev1alpha1.Flare)) {
		f := &corev1alpha1.Flare{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: corev1alpha1.FlareSpec{
				UniverseRef: "u", GalaxyRef: "g", AstralRef: "a", Action: action,
			},
		}
		if mutate != nil {
			mutate(f)
		}
		Expect(k8sClient.Create(ctx, f)).To(Succeed())
	}

	reconcileFlare := func(name string) ctrl.Result {
		res, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: ns, Name: name},
		})
		Expect(err).NotTo(HaveOccurred())
		return res
	}

	getFlare := func(name string) *corev1alpha1.Flare {
		var f corev1alpha1.Flare
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &f)).To(Succeed())
		return &f
	}

	getJob := func(name string) *batchv1.Job {
		var j batchv1.Job
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &j)).To(Succeed())
		return &j
	}

	requestOf := func(job *batchv1.Job) *runspec.Request {
		for _, e := range job.Spec.Template.Spec.Containers[0].Env {
			if e.Name == runspec.EnvRequest {
				req, err := runspec.DecodeRequest(e.Value)
				Expect(err).NotTo(HaveOccurred())
				return req
			}
		}
		Fail("the Job carries no run request")
		return nil
	}

	// finishJob writes what the runner would have logged, then marks the Job
	// the way the Job controller does.
	finishJob := func(name string, result *runspec.Result, failedReason string) {
		job := getJob(name)
		if result != nil {
			lines, err := runspec.ResultLines(nonceFromJob(job), result)
			Expect(err).NotTo(HaveOccurred())
			logs.set(name, runspec.StepLine(nonceFromJob(job), result.Step)+"\n"+strings.Join(lines, "\n")+"\n")
		}
		now := metav1.Now()
		job.Status.StartTime = &now
		if failedReason == "" {
			job.Status.Succeeded = 1
			job.Status.CompletionTime = &now
			job.Status.Conditions = []batchv1.JobCondition{
				{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: now},
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: now},
			}
		} else {
			job.Status.Failed = 1
			job.Status.Conditions = []batchv1.JobCondition{
				{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: failedReason, LastTransitionTime: now},
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: failedReason, LastTransitionTime: now},
			}
		}
		Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())
	}

	BeforeEach(func() {
		ns = newNamespace()
		logs = &fakeLogs{}
		reconciler = &FlareReconciler{
			Client:    k8sClient,
			Scheme:    scheme.Scheme,
			APIReader: k8sClient,
			Logs:      logs,
			Runner:    RunnerConfig{Image: "runner:test", PullPolicy: corev1.PullIfNotPresent},
		}
		makeGalaxy("g", nil)
	})

	It("runs a Plan in a Job the Flare owns, and records the drift it reports", func() {
		makeAstral("a", nil)
		makeFlare("plan", corev1alpha1.FlareActionPlan, nil)

		reconcileFlare("plan")
		flare := getFlare("plan")
		Expect(flare.Status.Phase).To(Equal(corev1alpha1.FlarePhaseRunning), flare.Status.Message)
		Expect(flare.Status.LockAcquiredAt).NotTo(BeNil(), "the run lock lives in the Flare status")
		Expect(flare.Status.Attempt).To(Equal(int32(1)))
		Expect(flare.Status.JobName).To(Equal("plan-1"))

		job := getJob("plan-1")
		Expect(job.OwnerReferences).To(HaveLen(1))
		Expect(job.OwnerReferences[0].Name).To(Equal("plan"))
		Expect(*job.Spec.BackoffLimit).To(BeZero(), "an apply must never be retried on its own")
		pod := job.Spec.Template.Spec
		Expect(*pod.AutomountServiceAccountToken).To(BeFalse(), "repository code gets no cluster credentials")
		Expect(*pod.Containers[0].SecurityContext.ReadOnlyRootFilesystem).To(BeTrue())
		Expect(pod.Containers[0].Image).To(Equal("runner:test"))
		req := requestOf(job)
		Expect(req.Action).To(Equal(corev1alpha1.FlareActionPlan))
		Expect(req.Engine).To(Equal(corev1alpha1.EngineTerraform))
		Expect(req.AllowedDestroy).To(BeEmpty())

		// Reconciling again while the Job runs neither duplicates nor fails it.
		reconcileFlare("plan")
		Expect(getFlare("plan").Status.Attempt).To(Equal(int32(1)))

		finishJob("plan-1", &runspec.Result{
			Succeeded:   true,
			Step:        corev1alpha1.FlareStepPlan,
			Revision:    strings.Repeat("a", 40),
			Message:     "Plan: 2 to create",
			PlanSummary: &corev1alpha1.PlanSummary{ToCreate: 2},
			Changes: []corev1alpha1.ResourceChange{
				{Address: "aws_s3_bucket.a", Action: corev1alpha1.ChangeActionCreate},
				{Address: "aws_s3_bucket.b", Action: corev1alpha1.ChangeActionCreate},
			},
		}, "")
		reconcileFlare("plan")

		flare = getFlare("plan")
		Expect(flare.Status.Phase).To(Equal(corev1alpha1.FlarePhaseSucceeded), flare.Status.Message)
		Expect(flare.Status.LockAcquiredAt).To(BeNil(), "a finished Flare releases the lock")
		Expect(flare.Status.PlanSummary.ToCreate).To(Equal(int32(2)))
		Expect(flare.Status.ResourceChanges).To(HaveLen(2))
		Expect(flare.Status.Revision).To(HaveLen(40))
	})

	It("keeps a second Flare waiting while the first one holds the lock", func() {
		makeAstral("a", nil)
		makeFlare("first", corev1alpha1.FlareActionPlan, nil)
		makeFlare("second", corev1alpha1.FlareActionPlan, nil)

		reconcileFlare("first")
		res := reconcileFlare("second")

		second := getFlare("second")
		Expect(second.Status.Phase).To(Equal(corev1alpha1.FlarePhasePending), second.Status.Message)
		Expect(second.Status.LockAcquiredAt).To(BeNil())
		Expect(second.Status.JobName).To(BeEmpty())
		Expect(res.RequeueAfter).To(Equal(lockRetryInterval))
		started := findCondition(second.Status.Conditions, corev1alpha1.ConditionStarted)
		Expect(started.Reason).To(Equal(reasonLockBusy))
		Expect(started.Message).To(ContainSubstring("first"))

		finishJob("first-1", &runspec.Result{Succeeded: true, Step: corev1alpha1.FlareStepPlan,
			PlanSummary: &corev1alpha1.PlanSummary{}}, "")
		reconcileFlare("first")
		reconcileFlare("second")
		Expect(getFlare("second").Status.JobName).To(Equal("second-1"))
	})

	It("holds an apply that destroys until confirmDestroy names the Astral", func() {
		makeAstral("a", nil)
		makeFlare("apply", corev1alpha1.FlareActionApply, nil)

		reconcileFlare("apply")
		finishJob("apply-1", &runspec.Result{
			Succeeded:         true,
			NeedsConfirmation: true,
			Step:              corev1alpha1.FlareStepPlan,
			PlanSummary:       &corev1alpha1.PlanSummary{ToDestroy: 1, ToReplace: 1},
			PendingDestroy:    []string{"aws_db_instance.main", "aws_s3_bucket.logs"},
		}, "")
		reconcileFlare("apply")

		flare := getFlare("apply")
		Expect(flare.Status.Phase).To(Equal(corev1alpha1.FlarePhaseAwaitingConfirmation), flare.Status.Message)
		Expect(flare.Status.PendingDestroy).To(ConsistOf("aws_db_instance.main", "aws_s3_bucket.logs"))
		Expect(flare.Status.LockAcquiredAt).To(BeNil(), "nothing runs while a human decides")
		Expect(flare.Status.Message).To(ContainSubstring(`"a"`))

		// A confirmation that does not name the Astral changes nothing.
		flare.Spec.ConfirmDestroy = "yes"
		Expect(k8sClient.Update(ctx, flare)).To(Succeed())
		reconcileFlare("apply")
		flare = getFlare("apply")
		Expect(flare.Status.Phase).To(Equal(corev1alpha1.FlarePhaseAwaitingConfirmation), flare.Status.Message)
		Expect(findCondition(flare.Status.Conditions, corev1alpha1.ConditionDestroyConfirmed).Reason).
			To(Equal(reasonConfirmationMismatch))

		flare.Spec.ConfirmDestroy = "a"
		Expect(k8sClient.Update(ctx, flare)).To(Succeed())
		reconcileFlare("apply")
		flare = getFlare("apply")
		Expect(flare.Status.Phase).To(Equal(corev1alpha1.FlarePhaseRunning), flare.Status.Message)
		Expect(flare.Status.Attempt).To(Equal(int32(2)))
		Expect(flare.Status.JobName).To(Equal("apply-2"))
		Expect(requestOf(getJob("apply-2")).AllowedDestroy).
			To(ConsistOf("aws_db_instance.main", "aws_s3_bucket.logs"),
				"the second Job may destroy exactly what was confirmed, nothing more")

		finishJob("apply-2", &runspec.Result{
			Succeeded: true, Applied: true, Step: corev1alpha1.FlareStepApply,
			PlanSummary: &corev1alpha1.PlanSummary{ToDestroy: 1, ToReplace: 1},
		}, "")
		reconcileFlare("apply")
		flare = getFlare("apply")
		Expect(flare.Status.Phase).To(Equal(corev1alpha1.FlarePhaseSucceeded), flare.Status.Message)
		Expect(flare.Status.Resources.Deleted).To(Equal(int32(2)))
	})

	It("turns a Job that hit its deadline into a Cancelled Flare", func() {
		makeAstral("a", nil)
		makeFlare("slow", corev1alpha1.FlareActionPlan, nil)
		reconcileFlare("slow")
		finishJob("slow-1", nil, batchv1.JobReasonDeadlineExceeded)
		reconcileFlare("slow")

		flare := getFlare("slow")
		Expect(flare.Status.Phase).To(Equal(corev1alpha1.FlarePhaseCancelled), flare.Status.Message)
		Expect(flare.Status.LockAcquiredAt).To(BeNil())
		Expect(findCondition(flare.Status.Conditions, corev1alpha1.ConditionCompleted).Reason).
			To(Equal(reasonTimedOut))
	})

	It("cancels by deleting the Job", func() {
		makeAstral("a", nil)
		makeFlare("stop", corev1alpha1.FlareActionApply, nil)
		reconcileFlare("stop")

		flare := getFlare("stop")
		flare.Spec.Cancel = true
		Expect(k8sClient.Update(ctx, flare)).To(Succeed())
		reconcileFlare("stop")

		Expect(getFlare("stop").Status.Phase).To(Equal(corev1alpha1.FlarePhaseCancelled), getFlare("stop").Status.Message)
		var job batchv1.Job
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "stop-1"}, &job)
		if err == nil {
			Expect(job.DeletionTimestamp).NotTo(BeNil(), "the Job must be on its way out")
		}
	})

	It("refuses a whole-stack apply while a unit is failing", func() {
		a := makeAstral("a", func(a *corev1alpha1.Astral) {
			a.Spec.Terragrunt = &corev1alpha1.TerragruntSpec{Enabled: true, Mode: corev1alpha1.TerragruntModeRunAll}
		})
		a.Status.Units = []corev1alpha1.UnitStatus{
			{Path: "data/cloudsql", Result: corev1alpha1.UnitResultFailed, SyncStatus: corev1alpha1.SyncStatusUnknown},
			{Path: "apps/web", Result: corev1alpha1.UnitResultSucceeded, SyncStatus: corev1alpha1.SyncStatusOutOfSync},
		}
		Expect(k8sClient.Status().Update(ctx, a)).To(Succeed())

		makeFlare("all", corev1alpha1.FlareActionApply, nil)
		reconcileFlare("all")
		flare := getFlare("all")
		Expect(flare.Status.Phase).To(Equal(corev1alpha1.FlarePhaseFailed), flare.Status.Message)
		Expect(flare.Status.Message).To(ContainSubstring("data/cloudsql"))
		Expect(flare.Status.JobName).To(BeEmpty(), "nothing was launched")

		// The healthy unit can still be reconciled on its own.
		makeFlare("one", corev1alpha1.FlareActionApply, func(f *corev1alpha1.Flare) {
			f.Spec.Scope = corev1alpha1.FlareScopeUnit
			f.Spec.UnitPath = "apps/web"
		})
		reconcileFlare("one")
		Expect(getFlare("one").Status.JobName).To(Equal("one-1"))
		req := requestOf(getJob("one-1"))
		Expect(req.Scope).To(Equal(corev1alpha1.FlareScopeUnit))
		Expect(req.UnitPath).To(Equal("apps/web"))
	})

	It("rejects a unit scope without a unit path at admission", func() {
		makeAstral("a", nil)
		f := &corev1alpha1.Flare{
			ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: ns},
			Spec: corev1alpha1.FlareSpec{
				UniverseRef: "u", GalaxyRef: "g", AstralRef: "a",
				Action: corev1alpha1.FlareActionPlan, Scope: corev1alpha1.FlareScopeUnit,
			},
		}
		err := k8sClient.Create(ctx, f)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("unitPath is required when scope is Unit"))
	})

	It("passes cloud credentials as env from their Secret, never through the request", func() {
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "cloud", Namespace: ns},
			Data:       map[string][]byte{"AWS_SECRET_ACCESS_KEY": []byte("s3cr3t-value")},
		})).To(Succeed())
		makeAstral("a", func(a *corev1alpha1.Astral) {
			a.Spec.Secrets = []corev1alpha1.EnvSecretRef{{Name: "cloud", EnvPrefix: ptr.To("")}}
		})
		makeFlare("p", corev1alpha1.FlareActionPlan, nil)
		reconcileFlare("p")

		job := getJob("p-1")
		container := job.Spec.Template.Spec.Containers[0]
		Expect(container.EnvFrom).To(HaveLen(1))
		Expect(container.EnvFrom[0].SecretRef.Name).To(Equal("cloud"))
		for _, e := range container.Env {
			Expect(e.Value).NotTo(ContainSubstring("s3cr3t-value"))
		}
		Expect(requestOf(job).SecretEnv).To(ContainElement("AWS_SECRET_ACCESS_KEY"),
			"the runner must know which values to mask")
	})

	It("exchanges a GitHub App for a token so the App key never reaches the pod", func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		Expect(err).NotTo(HaveOccurred())
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

		github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v3/repos/4sh/cinng-iac/installation":
				_, _ = fmt.Fprint(w, `{"id": 7}`)
			case "/api/v3/app/installations/7/access_tokens":
				_, _ = fmt.Fprint(w, `{"token": "ghs_minted"}`)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer github.Close()
		reconciler.HTTPClient = github.Client()

		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: ns},
			Data: map[string][]byte{
				shared.SecretKeyGitHubAppID:         []byte("123456"),
				shared.SecretKeyGitHubAppPrivateKey: keyPEM,
			},
		})).To(Succeed())
		makeAstral("a", func(a *corev1alpha1.Astral) {
			a.Spec.VCSRepository.URL = github.URL + "/4sh/cinng-iac"
			a.Spec.VCSRepository.SecretRef = &corev1alpha1.SecretRef{Name: "app"}
		})
		makeFlare("p", corev1alpha1.FlareActionPlan, nil)
		reconcileFlare("p")

		job := getJob("p-1")
		var mounted string
		for _, v := range job.Spec.Template.Spec.Volumes {
			if v.Secret != nil {
				mounted = v.Secret.SecretName
			}
		}
		Expect(mounted).To(Equal("p-1-git"), "the Job mounts the minted token, not the App Secret")

		var minted corev1.Secret
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: mounted}, &minted)).To(Succeed())
		Expect(string(minted.Data[shared.SecretKeyPassword])).To(Equal("ghs_minted"))
		Expect(minted.Data).NotTo(HaveKey(shared.SecretKeyGitHubAppPrivateKey))
		Expect(minted.OwnerReferences[0].Name).To(Equal("p"), "the token goes away with its Flare")
	})
})

func findCondition(conditions []metav1.Condition, condType string) metav1.Condition {
	for _, c := range conditions {
		if c.Type == condType {
			return c
		}
	}
	Fail("condition " + condType + " not found")
	return metav1.Condition{}
}
