// /*
// Copyright 2025 The Grove Authors.
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

package startup

import (
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
)

// OnRetryFunc is called before each retry attempt (after the first failure).
// It receives the attempt number (1-indexed) and the error from the previous attempt.
type OnRetryFunc func(attempt int, lastErr error)

// Retrier holds retry configuration. Create one per logical operation or share
// a single instance across all calls with the same policy.
type Retrier struct {
	backoff     wait.Backoff
	isRetryable func(error) bool
	onRetry     OnRetryFunc
}

// New creates a Retrier. Pass nil for onRetry to skip logging.
func New(backoff wait.Backoff, isRetryable func(error) bool, onRetry OnRetryFunc) *Retrier {
	return &Retrier{backoff: backoff, isRetryable: isRetryable, onRetry: onRetry}
}

// Do retries fn using the configured backoff. Use a closure to capture any
// number of arguments — including ctx when it doesn't change between retries:
//
//	err := r.Do(func() error {
//	    return SynchronizeTopology(ctx, cl, logger, backends)
//	})
func (r *Retrier) Do(fn func() error) error {
	attempt := 0
	var lastErr error
	return retry.OnError(r.backoff, r.isRetryable, func() error {
		if attempt > 0 && r.onRetry != nil {
			r.onRetry(attempt, lastErr)
		}
		attempt++
		lastErr = fn()
		return lastErr
	})
}
