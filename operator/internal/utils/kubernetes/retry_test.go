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
	"io"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
)

// TestIsRetriableAPIError verifies that optimistic-lock conflicts and transient server-side
// failures are reported as retriable, while permanent errors, non-API errors and nil are not.
func TestIsRetriableAPIError(t *testing.T) {
	gr := schema.GroupResource{Group: "grove.io", Resource: "podcliques"}
	gk := schema.GroupKind{Group: "grove.io", Kind: "PodClique"}
	cause := errors.New("boom")

	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{name: "conflict is retriable", err: apierrors.NewConflict(gr, "x", cause), expected: true},
		{name: "server timeout is retriable", err: apierrors.NewServerTimeout(gr, "get", 1), expected: true},
		{name: "timeout is retriable", err: apierrors.NewTimeoutError("timed out", 1), expected: true},
		{name: "too many requests is retriable", err: apierrors.NewTooManyRequests("slow down", 1), expected: true},
		{name: "service unavailable is retriable", err: apierrors.NewServiceUnavailable("unavailable"), expected: true},
		{name: "internal error is retriable", err: apierrors.NewInternalError(cause), expected: true},
		{name: "not found is not retriable", err: apierrors.NewNotFound(gr, "x"), expected: false},
		{name: "bad request is not retriable", err: apierrors.NewBadRequest("bad"), expected: false},
		{name: "forbidden is not retriable", err: apierrors.NewForbidden(gr, "x", cause), expected: false},
		{name: "invalid is not retriable", err: apierrors.NewInvalid(gk, "x", nil), expected: false},
		{name: "non-API error is not retriable", err: errors.New("plain error"), expected: false},
		{name: "nil is not retriable", err: nil, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := IsRetriableAPIError(tt.err)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "net/http: TLS handshake timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// clientError wraps err the way client-go surfaces a transport failure: a *url.Error wrapped by
// the caller with additional context.
func clientError(err error) error {
	return fmt.Errorf("failed to list: %w", &url.Error{Op: "Get", URL: "https://10.96.0.1:443/apis", Err: err})
}

// TestIsTransientConnectionError verifies that transport-level failures to reach the API server
// are reported as transient, while API status errors, cancellation and other errors are not.
func TestIsTransientConnectionError(t *testing.T) {
	dialErr := func(errno syscall.Errno) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)}
	}

	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{name: "connection refused is transient", err: clientError(dialErr(syscall.ECONNREFUSED)), expected: true},
		{name: "connection reset is transient", err: clientError(dialErr(syscall.ECONNRESET)), expected: true},
		{name: "EOF is transient", err: clientError(io.EOF), expected: true},
		{name: "unexpected EOF is transient", err: clientError(io.ErrUnexpectedEOF), expected: true},
		{name: "network timeout is transient", err: clientError(timeoutError{}), expected: true},
		{name: "client timeout is transient", err: clientError(context.DeadlineExceeded), expected: true},
		{name: "bare connection refused is transient", err: syscall.ECONNREFUSED, expected: true},
		{name: "cancelled context is not transient", err: clientError(context.Canceled), expected: false},
		{name: "non-timeout network error is not transient", err: clientError(&net.OpError{Op: "dial", Err: errors.New("no route")}), expected: false},
		{name: "API status error is not transient", err: apierrors.NewServiceUnavailable("unavailable"), expected: false},
		{name: "plain error is not transient", err: errors.New("plain error"), expected: false},
		{name: "nil is not transient", err: nil, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, IsTransientConnectionError(tt.err))
		})
	}
}

// TestIsRetriableError verifies that both API status errors and connection errors are retriable,
// and permanent errors are not.
func TestIsRetriableError(t *testing.T) {
	gr := schema.GroupResource{Group: "grove.io", Resource: "clustertopologybindings"}

	assert.True(t, IsRetriableError(apierrors.NewInternalError(errors.New("boom"))))
	assert.True(t, IsRetriableError(clientError(syscall.ECONNREFUSED)))
	assert.False(t, IsRetriableError(apierrors.NewForbidden(gr, "x", errors.New("denied"))))
	assert.False(t, IsRetriableError(apierrors.NewUnauthorized("no token")))
	assert.False(t, IsRetriableError(nil))
}

// TestRetryOnError verifies the retry loop: when it retries, when it stops, which error it
// returns and how often it reports a retry.
func TestRetryOnError(t *testing.T) {
	transient := apierrors.NewServiceUnavailable("unavailable")
	permanent := apierrors.NewForbidden(schema.GroupResource{Resource: "x"}, "x", errors.New("denied"))
	backoff := wait.Backoff{Duration: time.Millisecond, Factor: 1.0, Steps: 3}

	tests := []struct {
		name            string
		results         []error
		expectedErr     error
		expectedCalls   int
		expectedRetries []int
	}{
		{name: "succeeds on the first attempt", results: []error{nil}, expectedCalls: 1},
		{name: "succeeds after transient errors", results: []error{transient, transient, nil}, expectedCalls: 3, expectedRetries: []int{1, 2}},
		{name: "does not retry a permanent error", results: []error{permanent}, expectedErr: permanent, expectedCalls: 1},
		{name: "stops at a permanent error after a transient one", results: []error{transient, permanent}, expectedErr: permanent, expectedCalls: 2, expectedRetries: []int{1}},
		{name: "returns the last error when the steps are exhausted", results: []error{transient, transient, transient}, expectedErr: transient, expectedCalls: 3, expectedRetries: []int{1, 2}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			var retries []int
			err := RetryOnError(context.Background(), backoff, IsRetriableError,
				func(attempt int, err error) {
					assert.Equal(t, transient, err)
					retries = append(retries, attempt)
				},
				func(context.Context) error {
					calls++
					return tt.results[calls-1]
				})

			if tt.expectedErr == nil {
				require.NoError(t, err)
			} else {
				assert.Equal(t, tt.expectedErr, err)
			}
			assert.Equal(t, tt.expectedCalls, calls)
			assert.Equal(t, tt.expectedRetries, retries)
		})
	}
}

// TestRetryOnErrorNilOnRetry verifies that the retry callback is optional.
func TestRetryOnErrorNilOnRetry(t *testing.T) {
	calls := 0
	err := RetryOnError(context.Background(), wait.Backoff{Duration: time.Millisecond, Steps: 2}, IsRetriableError, nil,
		func(context.Context) error {
			calls++
			if calls == 1 {
				return apierrors.NewInternalError(errors.New("boom"))
			}
			return nil
		})

	require.NoError(t, err)
	assert.Equal(t, 2, calls)
}

// TestRetryOnErrorStopsWhenContextIsCancelled verifies that cancelling the context interrupts the
// wait between attempts instead of sleeping through it.
func TestRetryOnErrorStopsWhenContextIsCancelled(t *testing.T) {
	transient := apierrors.NewServiceUnavailable("unavailable")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	start := time.Now()
	err := RetryOnError(ctx, wait.Backoff{Duration: time.Hour, Factor: 1.0, Steps: 5}, IsRetriableError,
		func(int, error) { cancel() },
		func(context.Context) error {
			calls++
			return transient
		})

	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, transient)
	assert.Equal(t, 1, calls)
	assert.Less(t, time.Since(start), time.Minute)
}
