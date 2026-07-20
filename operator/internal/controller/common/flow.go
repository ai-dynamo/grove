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
	"context"
	"errors"
	"time"

	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
)

// ReconcileStepFn is a function that performs a step in the reconcile flow.
type ReconcileStepFn[T component.GroveCustomResourceType] func(ctx context.Context, log logr.Logger, obj *T) ReconcileStepResult

// ReconcileStepResult holds the result of a reconcile step.
type ReconcileStepResult struct {
	result            ctrl.Result
	errs              []error
	continueReconcile bool
	description       string
}

// Result returns the result and error from the reconcile step.
func (r ReconcileStepResult) Result() (ctrl.Result, error) {
	return r.result, errors.Join(r.errs...)
}

// NeedsRequeue returns true if the reconcile step needs to be requeued.
// This will happen if there is an error or if the result is marked to be requeued.
func (r ReconcileStepResult) NeedsRequeue() bool {
	return len(r.errs) > 0 || r.result.Requeue || r.result.RequeueAfter > 0
}

// HasErrors returns true if there are errors from the reconcile step.
func (r ReconcileStepResult) HasErrors() bool {
	return len(r.errs) > 0
}

// GetErrors returns the errors from the reconcile step.
func (r ReconcileStepResult) GetErrors() []error {
	return r.errs
}

// GetDescription returns the description from the reconcile step.
func (r ReconcileStepResult) GetDescription() string {
	return r.description
}

// DoNotRequeue returns a ReconcileStepResult that does not requeue the reconciliation.
func DoNotRequeue() ReconcileStepResult {
	return ReconcileStepResult{
		continueReconcile: false,
		result:            ctrl.Result{Requeue: false},
	}
}

// RecordErrorAndDoNotRequeue returns a ReconcileStepResult that records the error and does not requeue the reconciliation.
func RecordErrorAndDoNotRequeue(description string, errs ...error) ReconcileStepResult {
	return ReconcileStepResult{
		continueReconcile: false,
		result:            ctrl.Result{Requeue: false},
		errs:              errs,
		description:       description,
	}
}

// ContinueReconcile returns a ReconcileStepResult that continues the reconciliation to the next step.
func ContinueReconcile() ReconcileStepResult {
	return ReconcileStepResult{
		continueReconcile: true,
	}
}

// ReconcileWithErrors returns a ReconcileStepResult that re-queues the reconciliation with the given errors.
func ReconcileWithErrors(description string, errs ...error) ReconcileStepResult {
	return ReconcileStepResult{
		errs:              errs,
		continueReconcile: false,
		description:       description,
	}
}

// ReconcileAfter returns a ReconcileStepResult that re-queues the reconciliation after the given duration.
func ReconcileAfter(duration time.Duration, description string) ReconcileStepResult {
	return ReconcileStepResult{
		result: ctrl.Result{
			RequeueAfter: duration,
		},
		continueReconcile: false,
		description:       description,
	}
}

// ShortCircuitReconcileFlow returns true if the reconcile flow should be short-circuited and not continue.
func ShortCircuitReconcileFlow(result ReconcileStepResult) bool {
	return !result.continueReconcile
}
