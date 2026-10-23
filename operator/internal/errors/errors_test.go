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

package errors

import (
	"errors"
	"fmt"
	"testing"
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestWrapError(t *testing.T) {
	cause := fmt.Errorf("test error")
	const (
		code      = "ERR_TEST"
		operation = "test-operation"
		message   = "test message"
	)
	err := WrapError(cause, code, operation, message)
	groveErr := &GroveError{}
	assert.True(t, errors.As(err, &groveErr))
	assert.Equal(t, cause, groveErr.Cause)
	assert.Equal(t, code, string(groveErr.Code))
	assert.Equal(t, operation, groveErr.Operation)
	assert.Equal(t, message, groveErr.Message)
}

func TestMapToLastErrors(t *testing.T) {
	err1 := &GroveError{
		Code:       grovecorev1alpha1.ErrorCode("ERR_TEST1"),
		Cause:      fmt.Errorf("test-error1"),
		Operation:  "test-op",
		Message:    "test-message1",
		ObservedAt: time.Now().UTC(),
	}
	err2 := &GroveError{
		Code:       grovecorev1alpha1.ErrorCode("ERR_TEST2"),
		Cause:      fmt.Errorf("test-error2"),
		Operation:  "test-op",
		Message:    "test-message2",
		ObservedAt: time.Now().UTC(),
	}
	testCases := []struct {
		name             string
		errs             []error
		expectedLastErrs []grovecorev1alpha1.LastError
	}{
		{
			name:             "No errors",
			errs:             []error{},
			expectedLastErrs: []grovecorev1alpha1.LastError{},
		},
		{
			name: "A slice of Grove errors",
			errs: []error{err1, err2},
			expectedLastErrs: []grovecorev1alpha1.LastError{
				{Code: err1.Code, Description: err1.Error(), ObservedAt: metav1.NewTime(err1.ObservedAt)},
				{Code: err2.Code, Description: err2.Error(), ObservedAt: metav1.NewTime(err2.ObservedAt)},
			},
		},
		{
			name: "A slice of Grove errors and other errors",
			errs: []error{err1, err2, fmt.Errorf("test-error3")},
			expectedLastErrs: []grovecorev1alpha1.LastError{
				{Code: err1.Code, Description: err1.Error(), ObservedAt: metav1.NewTime(err1.ObservedAt)},
				{Code: err2.Code, Description: err2.Error(), ObservedAt: metav1.NewTime(err2.ObservedAt)},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			lastErrs := MapToLastErrors(tc.errs)
			assert.ElementsMatch(t, tc.expectedLastErrs, lastErrs)
		})
	}
}

func TestNewRequeueAfter(t *testing.T) {
	const (
		operation = "test-operation"
		message   = "retry later"
	)
	const requeueAfter = 2 * time.Second

	err := NewRequeueAfter(operation, message, requeueAfter)

	var groveErr *GroveError
	require.ErrorAs(t, err, &groveErr)
	assert.Equal(t, errCodeRequeueAfter, groveErr.Code)
	assert.Equal(t, operation, groveErr.Operation)
	assert.Equal(t, message, groveErr.Message)
	assert.False(t, groveErr.ObservedAt.IsZero())
	got, ok := GetRequeueAfter(err)
	require.True(t, ok)
	assert.Equal(t, requeueAfter, got)
}

func TestGetRequeueAfter(t *testing.T) {
	requeueErr := NewRequeueAfter("test-operation", "retry later", 3*time.Second)
	tests := []struct {
		name string
		err  error
		want time.Duration
		ok   bool
	}{
		{name: "direct", err: requeueErr, want: 3 * time.Second, ok: true},
		{name: "wrapped", err: fmt.Errorf("wrapped: %w", requeueErr), want: 3 * time.Second, ok: true},
		{name: "ordinary Grove error", err: New(grovecorev1alpha1.ErrorCode("ERR_TEST"), "test-operation", "failed")},
		{name: "plain error", err: errors.New("failed")},
		{name: "continue reconcile", err: NewContinueReconcileAndRequeue("test-operation", "continue")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := GetRequeueAfter(tt.err)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetLowestRequeueAfter(t *testing.T) {
	requeueAfter2s := NewRequeueAfter("test-operation", "retry in two seconds", 2*time.Second)
	requeueAfter5s := NewRequeueAfter("test-operation", "retry in five seconds", 5*time.Second)
	tests := []struct {
		name string
		errs []error
		want time.Duration
		ok   bool
	}{
		{name: "no errors"},
		{name: "no requeue errors", errs: []error{errors.New("failed")}},
		{name: "one requeue error", errs: []error{requeueAfter5s}, want: 5 * time.Second, ok: true},
		{name: "lowest mixed duration", errs: []error{requeueAfter5s, errors.New("failed"), requeueAfter2s}, want: 2 * time.Second, ok: true},
		{name: "wrapped requeue error", errs: []error{fmt.Errorf("wrapped: %w", requeueAfter2s)}, want: 2 * time.Second, ok: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := GetLowestRequeueAfter(tt.errs)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsRequeueAfter(t *testing.T) {
	requeueErr := NewRequeueAfter("test-operation", "retry later", 3*time.Second)
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "direct", err: requeueErr, want: true},
		{name: "wrapped", err: fmt.Errorf("wrapped: %w", requeueErr), want: true},
		{name: "continue reconcile", err: NewContinueReconcileAndRequeue("test-operation", "continue")},
		{name: "ordinary Grove error", err: New(grovecorev1alpha1.ErrorCode("ERR_TEST"), "test-operation", "failed")},
		{name: "plain error", err: errors.New("failed")},
		{name: "nil error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsRequeueAfter(tt.err))
		})
	}
}

func TestNewContinueReconcileAndRequeue(t *testing.T) {
	const (
		operation = "test-operation"
		message   = "continue and retry"
	)

	err := NewContinueReconcileAndRequeue(operation, message)

	var groveErr *GroveError
	require.ErrorAs(t, err, &groveErr)
	assert.Equal(t, errCodeContinueReconcileAndRequeue, groveErr.Code)
	assert.Equal(t, operation, groveErr.Operation)
	assert.Equal(t, message, groveErr.Message)
	assert.False(t, groveErr.ObservedAt.IsZero())
	assert.True(t, IsContinueReconcileAndRequeue(err))
}

func TestIsContinueReconcileAndRequeue(t *testing.T) {
	continueErr := NewContinueReconcileAndRequeue("test-operation", "continue")
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "direct", err: continueErr, want: true},
		{name: "wrapped", err: fmt.Errorf("wrapped: %w", continueErr), want: true},
		{name: "requeue after", err: NewRequeueAfter("test-operation", "retry", time.Second), want: false},
		{name: "ordinary Grove error", err: New(grovecorev1alpha1.ErrorCode("ERR_TEST"), "test-operation", "failed"), want: false},
		{name: "plain error", err: errors.New("failed"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsContinueReconcileAndRequeue(tt.err))
		})
	}
}
