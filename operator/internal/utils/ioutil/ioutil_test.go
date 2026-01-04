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

package ioutil

import (
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCloseQuietly tests the CloseQuietly function with various scenarios.
func TestCloseQuietly(t *testing.T) {
	tests := []struct {
		name      string
		closer    io.Closer
		expectErr bool
		validate  func(t *testing.T, closer io.Closer)
	}{
		{
			name: "close without errors",
			closer: &mockCloser{
				closeErr: nil,
			},
			expectErr: false,
			validate: func(t *testing.T, closer io.Closer) {
				mock := closer.(*mockCloser)
				assert.True(t, mock.closed, "closer should be closed")
				assert.Equal(t, 1, mock.callCount, "Close should be called once")
			},
		},
		{
			name: "close with error",
			closer: &mockCloser{
				closeErr: errors.New("close failed"),
			},
			expectErr: true,
			validate: func(t *testing.T, closer io.Closer) {
				mock := closer.(*mockCloser)
				assert.True(t, mock.closed, "closer should be closed despite error")
				assert.Equal(t, 1, mock.callCount, "Close should be called once")
			},
		},
		{
			name:      "closer is nil",
			closer:    nil,
			expectErr: false,
			validate: func(_ *testing.T, _ io.Closer) {
				// No panic should occur, nothing to validate
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This should not panic regardless of error
			assert.NotPanics(t, func() {
				CloseQuietly(tt.closer)
			}, "CloseQuietly should not panic")

			if tt.validate != nil {
				tt.validate(t, tt.closer)
			}
		})
	}
}

// TestCloseQuietlyWithRealCloser tests with a more realistic closer.
func TestCloseQuietlyWithRealCloser(t *testing.T) {
	// Using a simple pipe as a real io.Closer
	r, w := io.Pipe()

	// Close the reader first
	assert.NotPanics(t, func() {
		CloseQuietly(r)
	})

	// Close the writer
	assert.NotPanics(t, func() {
		CloseQuietly(w)
	})

	// Try to close again (should not panic even if already closed)
	assert.NotPanics(t, func() {
		CloseQuietly(r)
		CloseQuietly(w)
	})
}

// TestCloseQuietlyWithPanic tests that CloseQuietly doesn't handle panics (only errors).
func TestCloseQuietlyWithPanic(t *testing.T) {
	closer := &panicCloser{}

	// CloseQuietly should not catch panics, only suppress errors
	assert.Panics(t, func() {
		CloseQuietly(closer)
	}, "CloseQuietly should not suppress panics, only errors")
}

// mockCloser is a mock implementation of io.Closer for testing.
type mockCloser struct {
	closed    bool
	closeErr  error
	callCount int
}

func (m *mockCloser) Close() error {
	m.callCount++
	m.closed = true
	return m.closeErr
}

// panicCloser is a closer that panics when closed.
type panicCloser struct{}

func (p *panicCloser) Close() error {
	panic("intentional panic")
}
