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

package common

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ctrl "sigs.k8s.io/controller-runtime"
)

// TestReconcileStepResult tests the ReconcileStepResult struct and its methods.
func TestReconcileStepResult(t *testing.T) {
	// Test Result method
	t.Run("Result method", func(t *testing.T) {
		err1 := errors.New("error 1")
		err2 := errors.New("error 2")
		result := ReconcileStepResult{
			result: ctrl.Result{RequeueAfter: 5 * time.Second},
			errs:   []error{err1, err2},
		}

		r, err := result.Result()
		assert.Equal(t, 5*time.Second, r.RequeueAfter)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "error 1")
		assert.Contains(t, err.Error(), "error 2")
	})

	// Test NeedsRequeue with errors
	t.Run("NeedsRequeue with errors", func(t *testing.T) {
		result := ReconcileStepResult{
			errs: []error{errors.New("some error")},
		}
		assert.True(t, result.NeedsRequeue())
	})

	// Test NeedsRequeue with RequeueAfter
	t.Run("NeedsRequeue with RequeueAfter", func(t *testing.T) {
		result := ReconcileStepResult{
			result: ctrl.Result{RequeueAfter: 5 * time.Second},
		}
		assert.True(t, result.NeedsRequeue())
	})

	// Test NeedsRequeue returns false
	t.Run("NeedsRequeue false", func(t *testing.T) {
		result := ReconcileStepResult{
			result: ctrl.Result{},
		}
		assert.False(t, result.NeedsRequeue())
	})

	// Test HasErrors
	t.Run("HasErrors true", func(t *testing.T) {
		result := ReconcileStepResult{
			errs: []error{errors.New("error")},
		}
		assert.True(t, result.HasErrors())
	})

	// Test HasErrors false
	t.Run("HasErrors false", func(t *testing.T) {
		result := ReconcileStepResult{}
		assert.False(t, result.HasErrors())
	})

	// Test GetErrors
	t.Run("GetErrors", func(t *testing.T) {
		err1 := errors.New("error 1")
		err2 := errors.New("error 2")
		result := ReconcileStepResult{
			errs: []error{err1, err2},
		}
		errs := result.GetErrors()
		assert.Len(t, errs, 2)
		assert.Equal(t, err1, errs[0])
		assert.Equal(t, err2, errs[1])
	})

	// Test GetDescription
	t.Run("GetDescription", func(t *testing.T) {
		result := ReconcileStepResult{
			description: "test description",
		}
		assert.Equal(t, "test description", result.GetDescription())
	})
}

// TestDoNotRequeue tests that DoNotRequeue creates the correct result structure.
func TestDoNotRequeue(t *testing.T) {
	result := DoNotRequeue()

	assert.False(t, result.continueReconcile)
	assert.Zero(t, result.result.RequeueAfter)
	assert.False(t, result.NeedsRequeue())
	assert.False(t, result.HasErrors())
}

// TestRecordErrorAndDoNotRequeue tests error recording without requeue.
func TestRecordErrorAndDoNotRequeue(t *testing.T) {
	err1 := errors.New("error 1")
	err2 := errors.New("error 2")
	result := RecordErrorAndDoNotRequeue("test description", err1, err2)

	assert.False(t, result.continueReconcile)
	assert.Zero(t, result.result.RequeueAfter)
	assert.True(t, result.HasErrors())
	assert.Len(t, result.GetErrors(), 2)
	assert.Equal(t, "test description", result.GetDescription())
	assert.True(t, result.NeedsRequeue()) // Has errors, so needs requeue
}

// TestContinueReconcile tests that ContinueReconcile sets the flag properly.
func TestContinueReconcile(t *testing.T) {
	result := ContinueReconcile()

	assert.True(t, result.continueReconcile)
	assert.False(t, result.NeedsRequeue())
	assert.False(t, result.HasErrors())
}

// TestReconcileWithErrors tests reconcile with errors.
func TestReconcileWithErrors(t *testing.T) {
	err1 := errors.New("error 1")
	err2 := errors.New("error 2")
	result := ReconcileWithErrors("test description", err1, err2)

	assert.False(t, result.continueReconcile)
	assert.True(t, result.result.IsZero())
	assert.True(t, result.HasErrors())
	assert.Len(t, result.GetErrors(), 2)
	assert.Equal(t, "test description", result.GetDescription())
	assert.True(t, result.NeedsRequeue())
}

// TestReconcileAfter tests reconcile with a delay.
func TestReconcileAfter(t *testing.T) {
	duration := 10 * time.Second
	result := ReconcileAfter(duration, "test description")

	assert.False(t, result.continueReconcile)
	assert.Equal(t, duration, result.result.RequeueAfter)
	assert.False(t, result.HasErrors())
	assert.Equal(t, "test description", result.GetDescription())
	assert.True(t, result.NeedsRequeue())
}

// TestShortCircuitReconcileFlow tests the short-circuit logic.
func TestShortCircuitReconcileFlow(t *testing.T) {
	// Test short circuit when continueReconcile is false
	t.Run("short circuit", func(t *testing.T) {
		result := DoNotRequeue()
		assert.True(t, ShortCircuitReconcileFlow(result))
	})

	// Test no short circuit when continueReconcile is true
	t.Run("no short circuit", func(t *testing.T) {
		result := ContinueReconcile()
		assert.False(t, ShortCircuitReconcileFlow(result))
	})

	// Test short circuit with errors
	t.Run("short circuit with errors", func(t *testing.T) {
		result := ReconcileWithErrors("error", errors.New("test"))
		assert.True(t, ShortCircuitReconcileFlow(result))
	})

	// Test short circuit with requeue after
	t.Run("short circuit with requeue after", func(t *testing.T) {
		result := ReconcileAfter(5*time.Second, "requeue")
		assert.True(t, ShortCircuitReconcileFlow(result))
	})
}

// TestMergeStepResults verifies that merging two step results keeps errors from either result,
// keeps the sooner of the two requeues, and continues only when both results continue.
func TestMergeStepResults(t *testing.T) {
	errSpec := errors.New("spec failed")
	errStatus := errors.New("status failed")

	tests := []struct {
		name             string
		first            ReconcileStepResult
		second           ReconcileStepResult
		wantContinue     bool
		wantErrContains  []string
		wantRequeueAfter time.Duration
	}{
		{
			name:         "both continue with no requeue",
			first:        ContinueReconcile(),
			second:       ContinueReconcile(),
			wantContinue: true,
		},
		{
			name:             "shorter requeue of the two wins",
			first:            ReconcileAfter(5*time.Second, "spec"),
			second:           ReconcileAfter(3*time.Minute, "status resync"),
			wantRequeueAfter: 5 * time.Second,
		},
		{
			name:             "requeue from one, continue from the other",
			first:            ReconcileAfter(5*time.Second, "spec"),
			second:           ContinueReconcile(),
			wantRequeueAfter: 5 * time.Second,
		},
		{
			name:            "errors from either result are kept",
			first:           ReconcileWithErrors("spec", errSpec),
			second:          ReconcileWithErrors("status", errStatus),
			wantErrContains: []string{"spec failed", "status failed"},
		},
		{
			name:             "spec error is kept even when status requeues",
			first:            ReconcileWithErrors("spec", errSpec),
			second:           ReconcileAfter(5*time.Second, "status conflict"),
			wantErrContains:  []string{"spec failed"},
			wantRequeueAfter: 5 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merged := MergeStepResults(tt.first, tt.second)
			res, err := merged.Result()

			assert.Equal(t, tt.wantContinue, !ShortCircuitReconcileFlow(merged), "continue mismatch")
			assert.Equal(t, tt.wantRequeueAfter, res.RequeueAfter, "RequeueAfter mismatch")
			if len(tt.wantErrContains) == 0 {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				for _, want := range tt.wantErrContains {
					assert.Contains(t, err.Error(), want)
				}
			}
		})
	}
}
