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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
	"github.com/nrx-ops/stellarCD/internal/runspec"
)

// runnerContainer is the name of the container in every Flare Job.
const runnerContainer = "runner"

// runnerUID is the non-root user the runner image runs as.
const runnerUID = int64(65532)

// LabelFlare names the Flare a Job or Secret belongs to.
const LabelFlare = "core.stellarcd.io/flare"

// DefaultJobTTLSeconds keeps a finished Job, and so its pod and log, for a day.
// The result is copied into the Flare status as soon as the Job finishes; the
// log is kept only for people who want to read the full engine output.
const DefaultJobTTLSeconds = int32(24 * 60 * 60)

// RunnerConfig is how the controller builds Flare Jobs.
type RunnerConfig struct {
	// Image is the runner image: the runner binary, git, Terraform, OpenTofu
	// and Terragrunt.
	Image string
	// PullPolicy is the image pull policy.
	PullPolicy corev1.PullPolicy
	// Resources bounds the runner container. Zero uses defaultRunnerResources.
	Resources corev1.ResourceRequirements
	// TTLSecondsAfterFinished is how long a finished Job is kept. Zero uses
	// DefaultJobTTLSeconds.
	TTLSecondsAfterFinished int32
}

// defaultRunnerResources is sized for a mid-size root module. Providers are
// separate processes and each holds its schema in memory, so a stack with many
// providers needs more; the limit is generous rather than tight because an
// OOM-killed apply leaves the state lock behind.
func defaultRunnerResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		},
	}
}

// jobInputs is everything the Job builder needs, resolved by the reconciler.
type jobInputs struct {
	flare   *corev1alpha1.Flare
	astral  *corev1alpha1.Astral
	galaxy  *corev1alpha1.Galaxy
	request *runspec.Request
	nonce   string
	// gitSecret is the Secret mounted as git credentials, or empty for an
	// anonymous clone.
	gitSecret string
	// backendConfigMap is the Galaxy's rendered backend configuration, or empty.
	backendConfigMap string
}

// JobName is the deterministic name of a Flare's Job for one attempt, so a
// reconcile that is interrupted after creating it finds it again.
func JobName(flare *corev1alpha1.Flare, attempt int32) string {
	// The Job controller copies the name into a pod label, capped at 63.
	return fmt.Sprintf("%s-%d", truncateTo(flare.Name, 56), attempt)
}

func truncateTo(name string, n int) string {
	if len(name) <= n {
		return name
	}
	return name[:n]
}

// newNonce returns the per-Job tag the runner puts on its markers.
func newNonce() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate a run nonce: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// buildJob renders the Job that executes one attempt of a Flare.
//
// The pod carries no ServiceAccount token: the code it runs comes from the
// repository, and nothing it does needs the Kubernetes API. Secrets reach it
// only through Kubernetes, as env vars for cloud credentials and as a mounted
// file for git, so they never transit through the Flare or the run request.
func buildJob(in jobInputs, cfg RunnerConfig) (*batchv1.Job, error) {
	encoded, err := in.request.Encode()
	if err != nil {
		return nil, err
	}

	flare := in.flare
	labels := managedLabels(flare.Spec.UniverseRef, flare.Spec.GalaxyRef, flare.Spec.AstralRef)
	labels[LabelFlare] = truncateTo(flare.Name, 63)

	env := []corev1.EnvVar{
		{Name: runspec.EnvRequest, Value: encoded},
		{Name: runspec.EnvNonce, Value: in.nonce},
		// The image's home is read-only; the engine writes its CLI config
		// and plugin bookkeeping under HOME.
		{Name: "HOME", Value: "/tmp"},
	}
	var envFrom []corev1.EnvFromSource
	for _, ref := range in.astral.Spec.Secrets {
		if ref.Key != "" {
			env = append(env, corev1.EnvVar{
				Name: ref.VarName(),
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
					Key:                  ref.Key,
				}},
			})
			continue
		}
		envFrom = append(envFrom, corev1.EnvFromSource{
			Prefix:    ref.Prefix(),
			SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name}},
		})
	}
	if ref := in.astral.Spec.BackendSecret(&in.galaxy.Spec); ref != nil {
		// Backend credentials keep their own key names: the engine looks for
		// GOOGLE_CREDENTIALS, AWS_ACCESS_KEY_ID and friends verbatim.
		envFrom = append(envFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name}},
		})
	}

	volumes := []corev1.Volume{
		{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	mounts := []corev1.VolumeMount{
		{Name: "work", MountPath: runspec.WorkDir},
		{Name: "tmp", MountPath: "/tmp"},
	}
	if in.gitSecret != "" {
		volumes = append(volumes, corev1.Volume{Name: "git", VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: in.gitSecret,
				// Group-readable through fsGroup, and nothing else.
				DefaultMode: ptr.To[int32](0o440),
			},
		}})
		mounts = append(mounts, corev1.VolumeMount{Name: "git", MountPath: runspec.GitCredentialsDir, ReadOnly: true})
	}
	if in.backendConfigMap != "" {
		volumes = append(volumes, corev1.Volume{Name: "backend", VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: in.backendConfigMap},
			},
		}})
		mounts = append(mounts, corev1.VolumeMount{Name: "backend", MountPath: runspec.BackendConfigDir, ReadOnly: true})
	}

	resources := cfg.Resources
	if len(resources.Requests) == 0 && len(resources.Limits) == 0 {
		resources = defaultRunnerResources()
	}
	ttl := cfg.TTLSecondsAfterFinished
	if ttl <= 0 {
		ttl = DefaultJobTTLSeconds
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      JobName(flare, flare.Status.Attempt),
			Namespace: flare.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			// An apply that failed halfway must never be retried on its own.
			BackoffLimit:            ptr.To[int32](0),
			ActiveDeadlineSeconds:   ptr.To(int64(flare.Spec.Timeout().Seconds())),
			TTLSecondsAfterFinished: ptr.To(ttl),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					Volumes:                      volumes,
					AutomountServiceAccountToken: ptr.To(false),
					EnableServiceLinks:           ptr.To(false),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   ptr.To(true),
						RunAsUser:      ptr.To(runnerUID),
						RunAsGroup:     ptr.To(runnerUID),
						FSGroup:        ptr.To(runnerUID),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:            runnerContainer,
						Image:           cfg.Image,
						ImagePullPolicy: cfg.PullPolicy,
						Env:             env,
						EnvFrom:         envFrom,
						VolumeMounts:    mounts,
						Resources:       resources,
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr.To(false),
							ReadOnlyRootFilesystem:   ptr.To(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
					}},
				},
			},
		},
	}
	return job, nil
}

// buildRequest renders the run request for one attempt. allowed is the destroy
// allowlist the user confirmed, empty on a first attempt.
func buildRequest(
	flare *corev1alpha1.Flare,
	astral *corev1alpha1.Astral,
	secretEnv []string,
	backendConfig bool,
	allowed []string,
) *runspec.Request {
	spec := astral.Spec
	req := &runspec.Request{
		Action:        flare.Spec.Action,
		Engine:        spec.Engine,
		EngineVersion: spec.EngineVersion,
		Scope:         flare.Spec.EffectiveScope(),
		UnitPath:      flare.Spec.UnitPath,
		Repository: runspec.Repository{
			URL:      spec.VCSRepository.URL,
			Branch:   spec.VCSRepository.Branch,
			Revision: flare.Spec.Revision,
			Path:     spec.VCSRepository.Path,
		},
		Workspace:         spec.WorkspaceName,
		Parallelism:       flare.Spec.EffectiveParallelism(),
		Lock:              spec.EnableLocking,
		RefreshBeforePlan: flare.Spec.RefreshBeforePlan,
		Variables:         mergedVariables(astral, flare),
		AllowedDestroy:    slices.Clone(allowed),
		SecretEnv:         secretEnv,
		BackendConfig:     backendConfig,
	}
	if req.Engine == "" {
		req.Engine = corev1alpha1.EngineTerraform
	}
	if tg := spec.Terragrunt; tg != nil && tg.Enabled {
		mode := tg.Mode
		if mode == "" {
			mode = corev1alpha1.TerragruntModeUnit
		}
		req.Terragrunt = &runspec.Terragrunt{
			Version:       tg.Version,
			Mode:          mode,
			ExcludedUnits: slices.Clone(tg.ExcludedUnits),
		}
	}
	if hooks := spec.CustomHooks; hooks != nil {
		req.Hooks = runspec.Hooks{
			PrePlan:   hooks.PrePlan,
			PostPlan:  hooks.PostPlan,
			PreApply:  hooks.PreApply,
			PostApply: hooks.PostApply,
		}
	}
	return req
}

// nonceFromJob reads back the nonce the Job was created with, so nothing has
// to be stored besides the Job itself.
func nonceFromJob(job *batchv1.Job) string {
	for _, c := range job.Spec.Template.Spec.Containers {
		if c.Name != runnerContainer {
			continue
		}
		for _, e := range c.Env {
			if e.Name == runspec.EnvNonce {
				return e.Value
			}
		}
	}
	return ""
}
