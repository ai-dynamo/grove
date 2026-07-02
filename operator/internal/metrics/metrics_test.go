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

package metrics

import (
	"fmt"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestRecordStatusUpdateConflict(t *testing.T) {
	ctrl, kind := "pcs-test-conflict", "PodCliqueSet"
	before := testutil.ToFloat64(statusUpdateConflictTotal.WithLabelValues(ctrl, kind))
	RecordStatusUpdateConflict(ctrl, kind)
	after := testutil.ToFloat64(statusUpdateConflictTotal.WithLabelValues(ctrl, kind))
	if after-before != 1 {
		t.Errorf("status_update_conflict_total delta = %v, want 1", after-before)
	}
}

func TestRecordReconcileError_Conflict(t *testing.T) {
	ctrl := "pcs-test-err-conflict"
	err := apierrors.NewConflict(schema.GroupResource{Group: "core.grove.io", Resource: "podcliqueset"}, "obj", fmt.Errorf("modified"))
	before := testutil.ToFloat64(reconcileErrorsTotal.WithLabelValues(ctrl, "conflict"))
	RecordReconcileError(ctrl, err)
	after := testutil.ToFloat64(reconcileErrorsTotal.WithLabelValues(ctrl, "conflict"))
	if after-before != 1 {
		t.Errorf("reconcile_errors_total{error_type=conflict} delta = %v, want 1", after-before)
	}
}

func TestRecordReconcileError_NotFound(t *testing.T) {
	ctrl := "pcs-test-err-notfound"
	err := apierrors.NewNotFound(schema.GroupResource{Resource: "podcliqueset"}, "obj")
	before := testutil.ToFloat64(reconcileErrorsTotal.WithLabelValues(ctrl, "not_found"))
	RecordReconcileError(ctrl, err)
	after := testutil.ToFloat64(reconcileErrorsTotal.WithLabelValues(ctrl, "not_found"))
	if after-before != 1 {
		t.Errorf("reconcile_errors_total{error_type=not_found} delta = %v, want 1", after-before)
	}
}

func TestRecordReconcileError_Unknown(t *testing.T) {
	ctrl := "pcs-test-err-unknown"
	before := testutil.ToFloat64(reconcileErrorsTotal.WithLabelValues(ctrl, "unknown"))
	RecordReconcileError(ctrl, fmt.Errorf("some unexpected error"))
	after := testutil.ToFloat64(reconcileErrorsTotal.WithLabelValues(ctrl, "unknown"))
	if after-before != 1 {
		t.Errorf("reconcile_errors_total{error_type=unknown} delta = %v, want 1", after-before)
	}
}

func TestRecordReconcileError_Nil(t *testing.T) {
	// Calling with nil should be a no-op; verify no panic and counter stays zero.
	ctrl := "pcs-test-err-nil"
	before := testutil.CollectAndCount(reconcileErrorsTotal)
	RecordReconcileError(ctrl, nil)
	after := testutil.CollectAndCount(reconcileErrorsTotal)
	if after != before {
		t.Errorf("reconcile_errors_total series count changed on nil error: before=%d after=%d", before, after)
	}
}
