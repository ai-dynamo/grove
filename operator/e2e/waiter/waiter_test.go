//go:build e2e

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

package waiter

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testPCS = &grovecorev1alpha1.PodCliqueSet{}

type pcsWaiter = Waiter[*grovecorev1alpha1.PodCliqueSet]

func TestWaitFor(t *testing.T) {
	fetchErr := errors.New("connection refused")

	tests := []struct {
		name            string
		waiter          *pcsWaiter
		ctx             func() context.Context
		fetchFn         FetchFunc[*grovecorev1alpha1.PodCliqueSet]
		wantResult      *grovecorev1alpha1.PodCliqueSet
		wantErr         bool
		wantErrIs       error
		wantErrContains string
	}{
		{
			name:   "immediate success",
			waiter: New[*grovecorev1alpha1.PodCliqueSet]().WithTimeout(100 * time.Millisecond).WithInterval(10 * time.Millisecond),
			fetchFn: func(_ context.Context) (*grovecorev1alpha1.PodCliqueSet, error) {
				return testPCS, nil
			},
			wantResult: testPCS,
		},
		{
			name:   "success after retries",
			waiter: New[*grovecorev1alpha1.PodCliqueSet]().WithTimeout(200 * time.Millisecond).WithInterval(10 * time.Millisecond).WithRetryOnError(),
			fetchFn: func() FetchFunc[*grovecorev1alpha1.PodCliqueSet] {
				var count atomic.Int32
				return func(_ context.Context) (*grovecorev1alpha1.PodCliqueSet, error) {
					if count.Add(1) < 3 {
						return nil, errors.New("not found")
					}
					return testPCS, nil
				}
			}(),
			wantResult: testPCS,
		},
		{
			name:   "timeout",
			waiter: New[*grovecorev1alpha1.PodCliqueSet]().WithTimeout(50 * time.Millisecond).WithInterval(10 * time.Millisecond),
			fetchFn: func(_ context.Context) (*grovecorev1alpha1.PodCliqueSet, error) {
				return nil, nil
			},
			wantErr:         true,
			wantErrContains: "timeout",
		},
		{
			name:   "fetch error fails immediately by default",
			waiter: New[*grovecorev1alpha1.PodCliqueSet]().WithTimeout(100 * time.Millisecond).WithInterval(10 * time.Millisecond),
			fetchFn: func(_ context.Context) (*grovecorev1alpha1.PodCliqueSet, error) {
				return nil, fetchErr
			},
			wantErr:   true,
			wantErrIs: fetchErr,
		},
		{
			name:   "fetch errors retried with WithRetryOnError",
			waiter: New[*grovecorev1alpha1.PodCliqueSet]().WithTimeout(200 * time.Millisecond).WithInterval(10 * time.Millisecond).WithRetryOnError(),
			fetchFn: func() FetchFunc[*grovecorev1alpha1.PodCliqueSet] {
				var count atomic.Int32
				return func(_ context.Context) (*grovecorev1alpha1.PodCliqueSet, error) {
					if count.Add(1) < 3 {
						return nil, errors.New("not ready")
					}
					return testPCS, nil
				}
			}(),
			wantResult: testPCS,
		},
		{
			name:   "context cancellation",
			waiter: New[*grovecorev1alpha1.PodCliqueSet]().WithTimeout(100 * time.Millisecond).WithInterval(10 * time.Millisecond),
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			fetchFn: func(_ context.Context) (*grovecorev1alpha1.PodCliqueSet, error) {
				return nil, nil
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.ctx != nil {
				ctx = tt.ctx()
			}

			result, err := tt.waiter.WaitFor(ctx, tt.fetchFn, IsNotZero[*grovecorev1alpha1.PodCliqueSet])

			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrIs != nil {
					assert.ErrorIs(t, err, tt.wantErrIs)
				}
				if tt.wantErrContains != "" {
					assert.Contains(t, err.Error(), tt.wantErrContains)
				}
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantResult, result)
			}
		})
	}
}

func TestWaitUntil_ImmediateSuccess(t *testing.T) {
	w := New[*grovecorev1alpha1.PodCliqueSet]().WithTimeout(100 * time.Millisecond).WithInterval(10 * time.Millisecond)
	err := w.WaitUntil(context.Background(),
		func(_ context.Context) (*grovecorev1alpha1.PodCliqueSet, error) { return testPCS, nil },
		IsNotZero[*grovecorev1alpha1.PodCliqueSet],
	)
	require.NoError(t, err)
}

func TestWaitUntil_SuccessAfterRetries(t *testing.T) {
	var count atomic.Int32
	w := New[*grovecorev1alpha1.PodCliqueSet]().WithTimeout(200 * time.Millisecond).WithInterval(10 * time.Millisecond).WithRetryOnError()
	err := w.WaitUntil(context.Background(),
		func(_ context.Context) (*grovecorev1alpha1.PodCliqueSet, error) {
			if count.Add(1) < 3 {
				return nil, errors.New("not found")
			}
			return testPCS, nil
		},
		IsNotZero[*grovecorev1alpha1.PodCliqueSet],
	)
	require.NoError(t, err)
}

func TestWaitUntil_Timeout(t *testing.T) {
	w := New[*grovecorev1alpha1.PodCliqueSet]().WithTimeout(50 * time.Millisecond).WithInterval(10 * time.Millisecond)
	err := w.WaitUntil(context.Background(),
		func(_ context.Context) (*grovecorev1alpha1.PodCliqueSet, error) { return nil, nil },
		IsNotZero[*grovecorev1alpha1.PodCliqueSet],
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
}

func TestWaitUntil_FetchError_Default(t *testing.T) {
	fetchErr := errors.New("broken")
	w := New[*grovecorev1alpha1.PodCliqueSet]().WithTimeout(100 * time.Millisecond).WithInterval(10 * time.Millisecond)
	err := w.WaitUntil(context.Background(),
		func(_ context.Context) (*grovecorev1alpha1.PodCliqueSet, error) { return nil, fetchErr },
		IsNotZero[*grovecorev1alpha1.PodCliqueSet],
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, fetchErr)
}

func TestWaitUntil_FetchError_RetryOnError(t *testing.T) {
	var count atomic.Int32
	w := New[*grovecorev1alpha1.PodCliqueSet]().
		WithTimeout(200 * time.Millisecond).
		WithInterval(10 * time.Millisecond).
		WithRetryOnError()
	err := w.WaitUntil(context.Background(),
		func(_ context.Context) (*grovecorev1alpha1.PodCliqueSet, error) {
			if count.Add(1) < 3 {
				return nil, errors.New("not ready")
			}
			return testPCS, nil
		},
		IsNotZero[*grovecorev1alpha1.PodCliqueSet],
	)
	require.NoError(t, err)
}

func TestDefaults(t *testing.T) {
	w := New[*grovecorev1alpha1.PodCliqueSet]()
	assert.Equal(t, DefaultTimeout, w.timeout)
	assert.Equal(t, DefaultInterval, w.interval)
	assert.Nil(t, w.logger)
	assert.False(t, w.retryOnError)
}
