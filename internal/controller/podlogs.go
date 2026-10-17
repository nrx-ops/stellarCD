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
	"io"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get

// maxLogBytes caps one log read. LimitBytes counts from the start of the tail
// window, so it has to be large enough never to cut off the result block at
// the end of it; a few megabytes covers the window with room to spare.
const maxLogBytes = 4 << 20

// ErrNoPod means the Job has not created its pod yet, or it is already gone.
var ErrNoPod = errors.New("the Job has no pod")

// PodLogReader reads the tail of a Job's runner log. It is an interface so the
// controller tests can feed it a log without a kubelet.
type PodLogReader interface {
	JobLog(ctx context.Context, namespace, job string, tailLines int64) (string, error)
}

// ClientsetLogReader reads logs through the core API. The controller-runtime
// client cannot stream logs, and listing pods through it would put every pod
// in the cluster into the manager's cache.
type ClientsetLogReader struct {
	Clientset kubernetes.Interface
}

// JobLog implements PodLogReader. With backoffLimit at 0 a Job has at most one
// pod, but a pod the node evicted can be replaced, so the newest one wins.
func (r *ClientsetLogReader) JobLog(ctx context.Context, namespace, job string, tailLines int64) (string, error) {
	pods, err := r.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "batch.kubernetes.io/job-name=" + job,
	})
	if err != nil {
		return "", fmt.Errorf("failed to list the pods of Job %q: %w", job, err)
	}
	var newest *corev1.Pod
	for i := range pods.Items {
		p := &pods.Items[i]
		if newest == nil || p.CreationTimestamp.After(newest.CreationTimestamp.Time) {
			newest = p
		}
	}
	if newest == nil {
		return "", ErrNoPod
	}

	limit := int64(maxLogBytes)
	stream, err := r.Clientset.CoreV1().Pods(namespace).GetLogs(newest.Name, &corev1.PodLogOptions{
		Container:  runnerContainer,
		TailLines:  &tailLines,
		LimitBytes: &limit,
	}).Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to read the log of pod %q: %w", newest.Name, err)
	}
	defer func() { _ = stream.Close() }()

	data, err := io.ReadAll(stream)
	if err != nil {
		return "", fmt.Errorf("failed to read the log of pod %q: %w", newest.Name, err)
	}
	return string(data), nil
}
