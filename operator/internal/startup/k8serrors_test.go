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
	"io"
	"net"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// mockTimeoutError is a net.Error that reports Timeout() == true, mimicking a
// transport-level dial timeout (e.g. API server rolling restart, network partition).
type mockTimeoutError struct{}

func (mockTimeoutError) Error() string { return "i/o timeout" }

func (mockTimeoutError) Timeout() bool { return true }

func (mockTimeoutError) Temporary() bool { return true }

func TestIsTransientAPIError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantRetry bool
	}{
		{
			name:      "apierrors.InternalError is transient",
			err:       apierrors.NewInternalError(fmt.Errorf("internal")),
			wantRetry: true,
		},
		{
			name:      "apierrors.ServiceUnavailable is transient",
			err:       apierrors.NewServiceUnavailable("unavailable"),
			wantRetry: true,
		},
		{
			name:      "apierrors.Forbidden is permanent",
			err:       apierrors.NewForbidden(schema.GroupResource{}, "", fmt.Errorf("denied")),
			wantRetry: false,
		},
		{
			name:      "net.Error timeout is transient",
			err:       mockTimeoutError{},
			wantRetry: true,
		},
		{
			name:      "io.EOF is transient",
			err:       io.EOF,
			wantRetry: true,
		},
		{
			name:      "ECONNREFUSED is transient",
			err:       syscall.ECONNREFUSED,
			wantRetry: true,
		},
		{
			name:      "wrapped net.Error timeout is transient",
			err:       fmt.Errorf("wrapped: %w", mockTimeoutError{}),
			wantRetry: true,
		},
		{
			name:      "wrapped ECONNREFUSED is transient",
			err:       fmt.Errorf("wrapped: %w", syscall.ECONNREFUSED),
			wantRetry: true,
		},
		{
			name:      "net.Error non-timeout is not transient",
			err:       &net.OpError{Err: fmt.Errorf("some non-timeout net error")},
			wantRetry: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantRetry, IsTransientAPIError(tc.err))
		})
	}
}
