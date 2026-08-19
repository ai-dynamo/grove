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
