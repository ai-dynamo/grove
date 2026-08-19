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
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ error = (*GroveError)(nil)

// ErrCodeRequeueAfter is a special error code that indicates that the current step should be re-queued after a certain time,
const ErrCodeRequeueAfter grovecorev1alpha1.ErrorCode = "ERR_REQUEUE_AFTER"

// ErrCodeContinueReconcileAndRequeue is a special error code that indicates that the Sync of a current component errored,
// but the following components should be attempted to be synced, and then the current step should be re-queued after a certain time.
const ErrCodeContinueReconcileAndRequeue grovecorev1alpha1.ErrorCode = "ERR_CONTINUE_RECONCILE_AND_REQUEUE"

// GroveError is a custom error type that should be used throughout grove which encapsulates
// the underline error (cause), uniquely identifiable error code, contextual information captured
// as operation during which an error occurred and any custom message.
type GroveError struct {
	// Code indicates the category of error.
	Code grovecorev1alpha1.ErrorCode
	// Cause is the underline error.
	Cause error
	// Operation is the semantic operation during which this error is created/wrapped.
	Operation string
	// Message is the custom message providing additional context for the error.
	Message string
	// ObservedAt is the time at which the error was observed.
	ObservedAt time.Time
}

func (e *GroveError) Error() string {
	errMsg := fmt.Sprintf("[Operation: %s, Code: %s] message: %s", e.Operation, e.Code, e.Message)
	if e.Cause != nil {
		errMsg += fmt.Sprintf(", cause: %s", e.Cause.Error())
	}
	return errMsg
}

// New creates a new GroveError with the given error code, operation and message.
// This function should be used to create a new error. If you wish to wrap an existing error then use WrapError instead.
func New(code grovecorev1alpha1.ErrorCode, operation string, message string) error {
	return &GroveError{
		Code:       code,
		Operation:  operation,
		Message:    message,
		ObservedAt: time.Now().UTC(),
	}
}

// WrapError wraps the given error with the provided error code, operation and message.
// If the err is nil then it returns nil.
func WrapError(err error, code grovecorev1alpha1.ErrorCode, operation string, message string) error {
	if err == nil {
		return nil
	}
	return &GroveError{
		Code:       code,
		Cause:      err,
		Operation:  operation,
		Message:    message,
		ObservedAt: time.Now().UTC(),
	}
}

// MapToLastErrors maps the given errors (that are GroveError's) to LastError slice.
func MapToLastErrors(errs []error) []grovecorev1alpha1.LastError {
	lastErrs := make([]grovecorev1alpha1.LastError, 0, len(errs))
	for _, err := range errs {
		groveErr := &GroveError{}
		if errors.As(err, &groveErr) {
			lastErr := grovecorev1alpha1.LastError{
				Code:        groveErr.Code,
				Description: err.Error(),
				ObservedAt:  metav1.NewTime(groveErr.ObservedAt),
			}
			lastErrs = append(lastErrs, lastErr)
		}
	}
	return lastErrs
}
