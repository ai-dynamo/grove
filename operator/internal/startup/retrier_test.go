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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
)

// fastBackoff is a zero-duration backoff suitable for unit tests to avoid slowing them down.
var fastBackoff = wait.Backoff{Duration: time.Millisecond, Factor: 1.0, Steps: 10}

func TestRetrier_SucceedsAfterTransientError(t *testing.T) {
	transientErr := apierrors.NewInternalError(fmt.Errorf("internal"))
	callCount := 0
	fn := func() error {
		callCount++
		if callCount <= 2 {
			return transientErr
		}
		return nil
	}

	retryCount := 0
	r := New(fastBackoff, IsTransientAPIError, func(attempt int, _ error) {
		retryCount++
		assert.Equal(t, callCount, attempt, "attempt number should match call count")
	})

	err := r.Do(fn)
	require.NoError(t, err)
	assert.Equal(t, 3, callCount, "expected 2 transient failures then 1 success")
	assert.Equal(t, 2, retryCount, "onRetry should be called twice (attempts 2 and 3)")
}

func TestRetrier_PermanentErrorNotRetried(t *testing.T) {
	permErr := apierrors.NewForbidden(schema.GroupResource{Resource: "test"}, "", fmt.Errorf("denied"))
	callCount := 0
	fn := func() error {
		callCount++
		return permErr
	}

	r := New(fastBackoff, IsTransientAPIError, nil)
	err := r.Do(fn)
	require.Error(t, err)
	assert.True(t, apierrors.IsForbidden(err), "expected Forbidden error to propagate")
	assert.Equal(t, 1, callCount, "permanent error should not be retried")
}

func TestRetrier_ExhaustsRetriesOnPersistentTransientError(t *testing.T) {
	transientErr := apierrors.NewServiceUnavailable("unavailable")
	callCount := 0
	fn := func() error {
		callCount++
		return transientErr
	}

	shortBackoff := wait.Backoff{Duration: time.Millisecond, Factor: 1.0, Steps: 3}
	r := New(shortBackoff, IsTransientAPIError, nil)
	err := r.Do(fn)
	require.Error(t, err, "should fail after exhausting retry budget")
	assert.True(t, apierrors.IsServiceUnavailable(err))
	assert.Equal(t, 3, callCount, "should exhaust all 3 steps")
}

func TestRetrier_NoOnRetryCallback(t *testing.T) {
	transientErr := apierrors.NewInternalError(fmt.Errorf("internal"))
	callCount := 0
	fn := func() error {
		callCount++
		if callCount <= 2 {
			return transientErr
		}
		return nil
	}

	// Pass nil for onRetry — should not panic.
	r := New(fastBackoff, IsTransientAPIError, nil)
	err := r.Do(fn)
	require.NoError(t, err)
	assert.Equal(t, 3, callCount)
}
