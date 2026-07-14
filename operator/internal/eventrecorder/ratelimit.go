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

package eventrecorder

import (
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
)

const rateLimiterGCThreshold = 1024

func rateLimitKey(object runtime.Object, reason string) string {
	if accessor, err := meta.Accessor(object); err == nil {
		return fmt.Sprintf("%s/%s/%s/%s", accessor.GetNamespace(), accessor.GetName(), accessor.GetUID(), reason)
	}
	return reason
}

type eventRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*limiterEntry
	limit    rate.Limit
	burst    int
	ttl      time.Duration
	now      func() time.Time
}

type limiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newEventRateLimiter(interval time.Duration, burst int) *eventRateLimiter {
	return &eventRateLimiter{
		limiters: make(map[string]*limiterEntry),
		limit:    rate.Every(interval),
		burst:    burst,
		ttl:      2 * interval,
		now:      time.Now,
	}
}

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

func newLimiters(config Config) map[int]*eventRateLimiter {
	limiters := make(map[int]*eventRateLimiter, len(config.Rules))
	for i, rule := range config.Rules {
		limiters[i] = newEventRateLimiter(rule.Interval, rule.Burst)
	}
	return limiters
}

func allowEvent(config Config, limiters map[int]*eventRateLimiter, object runtime.Object, eventType, reason string) bool {
	key := rateLimitKey(object, reason)
	for i, rule := range config.Rules {
		if rule.EventType != eventType {
			continue
		}
		if len(rule.Reasons) > 0 && !containsReason(rule.Reasons, reason) {
			continue
		}
		limiter := limiters[i]
		if limiter == nil {
			continue
		}
		if !limiter.allow(key) {
			return false
		}
	}
	return true
}

func containsReason(reasons []string, reason string) bool {
	for _, candidate := range reasons {
		if candidate == reason {
			return true
		}
	}
	return false
}
