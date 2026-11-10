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

package kubernetes

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apimachinery/pkg/util/wait"
)

// IsRetriableAPIError reports whether an API server error is worth retrying. It returns true for
// optimistic-lock conflicts and transient server-side failures such as timeouts, throttling,
// unavailability and internal errors. Permanent errors such as NotFound, Forbidden and Invalid
// return false so callers surface them immediately.
func IsRetriableAPIError(err error) bool {
	return apierrors.IsConflict(err) ||
		apierrors.IsServerTimeout(err) ||
		apierrors.IsTimeout(err) ||
		apierrors.IsTooManyRequests(err) ||
		apierrors.IsServiceUnavailable(err) ||
		apierrors.IsInternalError(err)
}

// IsTransientConnectionError reports whether err is a transport-level failure to reach the API
// server: a refused or reset connection, a dropped connection (EOF), or a network timeout such as
// a dial, TLS handshake or I/O timeout. These never become a StatusError, so IsRetriableAPIError
// does not match them. A cancelled context is not a connection error and returns false.
func IsTransientConnectionError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	return utilnet.IsConnectionRefused(err) ||
		utilnet.IsConnectionReset(err) ||
		utilnet.IsProbableEOF(err) ||
		utilnet.IsTimeout(err)
}

// IsRetriableError reports whether err is either a retriable API server error or a transient
// connection error.
func IsRetriableError(err error) bool {
	return IsRetriableAPIError(err) || IsTransientConnectionError(err)
}

// RetryOnError calls fn until it succeeds, returns an error that isRetriable rejects, the backoff
// steps are exhausted, or ctx is done. The wait between attempts is interruptible by ctx. onRetry,
// when not nil, is called before each wait with the number of the failed attempt (starting at 1)
// and its error. On exhaustion the last error from fn is returned.
func RetryOnError(ctx context.Context, backoff wait.Backoff, isRetriable func(error) bool, onRetry func(attempt int, err error), fn func(ctx context.Context) error) error {
	var lastErr error
	attempt := 0
	steps := backoff.Steps
	err := wait.ExponentialBackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
		attempt++
		lastErr = fn(ctx)
		if lastErr == nil {
			return true, nil
		}
		if !isRetriable(lastErr) {
			return false, lastErr
		}
		if onRetry != nil && attempt < steps {
			onRetry(attempt, lastErr)
		}
		return false, nil
	})
	switch {
	case err == nil:
		return nil
	case wait.Interrupted(err) && lastErr != nil:
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%w: last error: %w", ctxErr, lastErr)
		}
		return lastErr
	default:
		return err
	}
}
