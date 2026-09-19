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
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/nrx-ops/stellarCD/internal/controller/shared"
)

// RunOutcome is everything a finished engine run hands back to the reconciler.
type RunOutcome struct {
	// Revision is the commit the run executed against.
	Revision string
	// Result is the engine output and parsed counts.
	Result *shared.RunResult
	// PlanArtifact is the binary plan file, empty for non-plan actions.
	PlanArtifact []byte
}

// RunFunc performs the work of a single run.
type RunFunc func(ctx context.Context) (*RunOutcome, error)

// Run is one in-flight engine execution. A terraform apply can take an hour;
// blocking a reconcile worker for that long would starve every other object, so
// the run lives on its own goroutine and the reconciler polls this handle.
type Run struct {
	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.Mutex
	outcome   *RunOutcome
	err       error
	cancelled bool
	timedOut  bool
}

// Done reports whether the run has finished.
func (r *Run) Done() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// Cancel asks the run to stop. It is safe to call more than once.
func (r *Run) Cancel() {
	r.mu.Lock()
	r.cancelled = true
	r.mu.Unlock()
	r.cancel()
}

// Cancelled reports whether Cancel was called.
func (r *Run) Cancelled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancelled
}

// TimedOut reports whether the run was killed by its own deadline rather than
// by an explicit cancellation.
func (r *Run) TimedOut() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.timedOut
}

// Result returns the outcome of a finished run. Calling it before Done reports
// true returns an error rather than blocking.
func (r *Run) Result() (*RunOutcome, error) {
	if !r.Done() {
		return nil, errors.New("engine run is still in progress")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.outcome == nil && r.err == nil {
		return nil, errRunLost
	}
	return r.outcome, r.err
}

// RunRegistry tracks the live runs of the Flare controller.
//
// It also implements manager.Runnable: when the manager's context is cancelled
// on SIGTERM it cancels every run and waits for the engine processes to exit,
// which is what keeps a shutdown from orphaning a terraform apply mid-write.
type RunRegistry struct {
	mu   sync.Mutex
	runs map[types.UID]*Run
	wg   sync.WaitGroup
}

// NewRunRegistry returns an empty registry.
func NewRunRegistry() *RunRegistry {
	return &RunRegistry{runs: map[types.UID]*Run{}}
}

// Launch starts fn on its own goroutine under a timeout and records it against
// uid. Launching twice for the same uid is a no-op: the first run wins.
func (reg *RunRegistry) Launch(uid types.UID, timeout time.Duration, fn RunFunc) *Run {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	if reg.runs == nil {
		reg.runs = map[types.UID]*Run{}
	}
	if existing, ok := reg.runs[uid]; ok {
		return existing
	}

	// context.Background rather than the reconcile context: the run must
	// outlive the pass that started it. Its lifetime is bounded by the timeout
	// and by Shutdown.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	run := &Run{cancel: cancel, done: make(chan struct{})}
	reg.runs[uid] = run

	reg.wg.Add(1)
	go func() {
		defer reg.wg.Done()
		defer cancel()
		defer close(run.done)

		outcome, err := fn(ctx)

		run.mu.Lock()
		run.outcome, run.err = outcome, err
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			run.timedOut = true
		}
		run.mu.Unlock()
	}()
	return run
}

// Get returns the run recorded for uid, or nil.
func (reg *RunRegistry) Get(uid types.UID) *Run {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return reg.runs[uid]
}

// Forget drops a finished run. Its cancel func is called first so the context
// is released even if the run ended on its own.
func (reg *RunRegistry) Forget(uid types.UID) {
	reg.mu.Lock()
	run, ok := reg.runs[uid]
	delete(reg.runs, uid)
	reg.mu.Unlock()

	if ok {
		run.cancel()
	}
}

// Len reports how many runs are tracked.
func (reg *RunRegistry) Len() int {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return len(reg.runs)
}

// NeedLeaderElection reports that runs only happen on the elected leader: two
// replicas executing the same Flare would race on remote state.
func (reg *RunRegistry) NeedLeaderElection() bool { return true }

// Start blocks until ctx is cancelled, then aborts every live run and waits for
// the engine processes to exit. It implements manager.Runnable.
func (reg *RunRegistry) Start(ctx context.Context) error {
	<-ctx.Done()

	logger := log.FromContext(ctx)
	reg.mu.Lock()
	live := len(reg.runs)
	for _, run := range reg.runs {
		run.Cancel()
	}
	reg.mu.Unlock()

	if live > 0 {
		logger.Info("Aborting in-flight engine runs before shutdown", "runs", live)
	}
	reg.wg.Wait()
	return nil
}
