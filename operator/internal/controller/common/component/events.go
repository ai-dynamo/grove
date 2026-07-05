// /*
// Copyright 2026 The Grove Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// */

package component

import (
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	// noopSuccessEventInterval is the minimum time that must elapse between two repeated success
	// events emitted for steady-state (no-op) reconciles of the same object and reason. It bounds
	// the rate at which such events reach the API server without silencing them completely.
	noopSuccessEventInterval = 5 * time.Minute
	// noopSuccessEventBurst is the number of no-op success events allowed to pass back-to-back for a
	// given key before the interval based rate limiting takes effect. A burst of 1 ensures the first
	// occurrence is always surfaced.
	noopSuccessEventBurst = 1
	// rateLimiterGCThreshold is the number of tracked keys above which stale per-key limiters are
	// evicted, keeping the limiter's memory footprint bounded over the operator's lifetime.
	rateLimiterGCThreshold = 1024
)

// noopEventLimiter throttles repeated success events emitted for steady-state reconciles.
var noopEventLimiter = newEventRateLimiter(noopSuccessEventInterval, noopSuccessEventBurst)

// RecordCreateOrPatchSuccessEvent records a Normal event when CreateOrPatch reconciles the object.
//
// A create or update (OperationResultCreated / OperationResultUpdated) reflects an actual state
// change and is always recorded. Steady-state reconciles return OperationResultNone and would
// otherwise emit the same success event on every pass, spamming the API server. Rather than
// dropping those events entirely, they are rate-limited per object and reason so that they remain
// observable while their volume stays bounded.
func RecordCreateOrPatchSuccessEvent(eventRecorder record.EventRecorder, object runtime.Object, opResult controllerutil.OperationResult, reason, messageFmt string, args ...any) {
	message := fmt.Sprintf(messageFmt, args...)
	if opResult == controllerutil.OperationResultNone && !noopEventLimiter.allow(rateLimitKey(object, reason)) {
		return
	}
	eventRecorder.Event(object, corev1.EventTypeNormal, reason, message)
}

// rateLimitKey derives the rate-limiting key for an event from the object identity and the event
// reason. Objects that cannot be introspected fall back to keying by reason alone.
func rateLimitKey(object runtime.Object, reason string) string {
	if accessor, err := meta.Accessor(object); err == nil {
		return fmt.Sprintf("%s/%s/%s/%s", accessor.GetNamespace(), accessor.GetName(), accessor.GetUID(), reason)
	}
	return reason
}

// eventRateLimiter maintains a token-bucket rate limiter per key so that repeated events for the
// same key are throttled while distinct keys are limited independently.
type eventRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*limiterEntry
	limit    rate.Limit
	burst    int
	ttl      time.Duration
	now      func() time.Time
}

// limiterEntry is a per-key token-bucket limiter along with the last time it was accessed, used to
// evict entries that are no longer active.
type limiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// newEventRateLimiter returns an eventRateLimiter that allows burst events immediately and then one
// additional event per interval for each key.
func newEventRateLimiter(interval time.Duration, burst int) *eventRateLimiter {
	return &eventRateLimiter{
		limiters: make(map[string]*limiterEntry),
		limit:    rate.Every(interval),
		burst:    burst,
		ttl:      2 * interval,
		now:      time.Now,
	}
}

// allow reports whether an event for the given key may be recorded now, consuming a token when it
// returns true.
func (l *eventRateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.gcLocked(now)

	entry, ok := l.limiters[key]
	if !ok {
		entry = &limiterEntry{limiter: rate.NewLimiter(l.limit, l.burst)}
		l.limiters[key] = entry
	}
	entry.lastSeen = now
	return entry.limiter.AllowN(now, 1)
}

// gcLocked evicts limiters that have not been accessed within the configured TTL once the number of
// tracked keys grows past the threshold. The caller must hold l.mu.
func (l *eventRateLimiter) gcLocked(now time.Time) {
	if len(l.limiters) <= rateLimiterGCThreshold {
		return
	}
	for key, entry := range l.limiters {
		if now.Sub(entry.lastSeen) > l.ttl {
			delete(l.limiters, key)
		}
	}
}
