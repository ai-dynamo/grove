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

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReconciledObject is an interface that defines the methods to track the status of a resource during reconciliation.
// It is expected that any resource that wishes to use the ReconcileErrorRecorder will need to have status fields for
// LastErrors, and it should also be a client.Object.
type ReconciledObject interface {
	client.Object
	// SetLastErrors sets the <resource>.Status.LastErrors on the target resource.
	SetLastErrors(lastErrors ...grovecorev1alpha1.LastError)
}

// ReconcileErrorRecorder is an interface that records errors during a reconciliation into <resource>.Status.LastErrors.
type ReconcileErrorRecorder interface {
	// RecordErrors records errors encountered during a reconcile run.
	RecordErrors(ctx context.Context, obj ReconciledObject, result *ReconcileStepResult) error
}

type recorder struct {
	client client.Client
}

// NewReconcileErrorRecorder returns a new reconcile status recorder.
func NewReconcileErrorRecorder(client client.Client) ReconcileErrorRecorder {
	return &recorder{
		client: client,
	}
}

// RecordErrors updates the target object's LastErrors status field with any errors from the reconcile step result
func (r *recorder) RecordErrors(ctx context.Context, obj ReconciledObject, result *ReconcileStepResult) error {
	var lastErrors []grovecorev1alpha1.LastError
	if result != nil && result.HasErrors() {
		lastErrors = groveerr.MapToLastErrors(result.GetErrors())
	}
	originalObj := obj.DeepCopyObject().(ReconciledObject)
	obj.SetLastErrors(lastErrors...)
	return r.client.Status().Patch(ctx, obj, client.MergeFrom(originalObj))
}
