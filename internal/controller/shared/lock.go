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

package shared

import "sync"

// StateLock serialises engine runs per remote state. Two Flares targeting the
// same Astral must never execute concurrently: the engine's own state lock
// would reject the second one mid-run, leaving a half-applied change and a
// stale lock behind.
//
// The lock is non-blocking on purpose. A reconciler that blocks holds one of a
// small pool of workers for the whole terraform run; instead TryAcquire fails
// fast and the caller requeues, which keeps the queue moving.
//
// Scope is one process. Multi-replica deployments rely on leader election, so
// only the leader ever runs a reconciler. Nothing here protects against a
// second operator installation pointed at the same state.
type StateLock struct {
	mu   sync.Mutex
	held map[string]string
}

// NewStateLock returns an empty lock registry.
func NewStateLock() *StateLock {
	return &StateLock{held: map[string]string{}}
}

// TryAcquire takes the lock for key on behalf of holder. It returns false when
// a different holder owns it. Re-acquiring with the same holder succeeds, so a
// reconciler that is requeued mid-run does not deadlock against itself.
func (l *StateLock) TryAcquire(key, holder string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held == nil {
		l.held = map[string]string{}
	}
	current, taken := l.held[key]
	if taken && current != holder {
		return false
	}
	l.held[key] = holder
	return true
}

// Release drops the lock if holder owns it. Releasing a lock owned by someone
// else is a no-op rather than a panic: a late cleanup path must not steal a
// live run's lock.
func (l *StateLock) Release(key, holder string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held[key] == holder {
		delete(l.held, key)
	}
}

// Holder returns the current owner of key, empty when free.
func (l *StateLock) Holder(key string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.held[key]
}
