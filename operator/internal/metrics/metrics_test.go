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
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type fakeReconciler struct {
	result ctrl.Result
	err    error
}

func (f *fakeReconciler) Reconcile(_ context.Context, _ reconcile.Request) (ctrl.Result, error) {
	return f.result, f.err
}

func TestObservedReconciler_Success(t *testing.T) {
	ctrlName := "t-success"
	observed := NewObservedReconciler(ctrlName, &fakeReconciler{result: ctrl.Result{}})

	_, err := observed.Reconcile(context.Background(), reconcile.Request{})
	if err != nil {
		t.Fatal(err)
	}

	if got := testutil.ToFloat64(reconcileTotal.WithLabelValues(ctrlName, "success")); got != 1 {
		t.Errorf("reconcile_total{result=success} = %v, want 1", got)
	}
	// in-flight gauge must be zero after the reconcile completes.
	if got := testutil.ToFloat64(inFlightReconciles.WithLabelValues(ctrlName)); got != 0 {
		t.Errorf("in_flight_reconciles = %v, want 0", got)
	}
}

func TestObservedReconciler_Error(t *testing.T) {
	ctrlName := "t-error"
	observed := NewObservedReconciler(ctrlName, &fakeReconciler{err: fmt.Errorf("inner error")})

	_, _ = observed.Reconcile(context.Background(), reconcile.Request{})

	if got := testutil.ToFloat64(reconcileTotal.WithLabelValues(ctrlName, "error")); got != 1 {
		t.Errorf("reconcile_total{result=error} = %v, want 1", got)
	}
}

func TestObservedReconciler_Conflict(t *testing.T) {
	ctrlName := "t-conflict"
	conflictErr := apierrors.NewConflict(
		schema.GroupResource{Group: "core.grove.io", Resource: "podcliqueset"},
		"my-pcs",
		fmt.Errorf("object modified"),
	)
	observed := NewObservedReconciler(ctrlName, &fakeReconciler{err: conflictErr})

	_, _ = observed.Reconcile(context.Background(), reconcile.Request{})

	if got := testutil.ToFloat64(conflictTotal.WithLabelValues(ctrlName)); got != 1 {
		t.Errorf("conflict_total = %v, want 1", got)
	}
	// conflicts are also counted as errors in reconcile_total.
	if got := testutil.ToFloat64(reconcileTotal.WithLabelValues(ctrlName, "error")); got != 1 {
		t.Errorf("reconcile_total{result=error} = %v, want 1 for conflict", got)
	}
}

func TestObservedReconciler_Requeue(t *testing.T) {
	ctrlName := "t-requeue"
	observed := NewObservedReconciler(ctrlName, &fakeReconciler{result: ctrl.Result{Requeue: true}})

	_, _ = observed.Reconcile(context.Background(), reconcile.Request{})

	if got := testutil.ToFloat64(reconcileTotal.WithLabelValues(ctrlName, "requeue")); got != 1 {
		t.Errorf("reconcile_total{result=requeue} = %v, want 1", got)
	}
}

func TestObservedReconciler_RequeueAfter(t *testing.T) {
	ctrlName := "t-requeue-after"
	observed := NewObservedReconciler(ctrlName, &fakeReconciler{result: ctrl.Result{RequeueAfter: 5 * time.Second}})

	_, _ = observed.Reconcile(context.Background(), reconcile.Request{})

	if got := testutil.ToFloat64(reconcileTotal.WithLabelValues(ctrlName, "requeue_after")); got != 1 {
		t.Errorf("reconcile_total{result=requeue_after} = %v, want 1", got)
	}
}

func TestStartOperation_PopulatesHistogram(t *testing.T) {
	// CollectAndCount returns the number of series present in the histogram vec.
	// Before calling StartOperation with a fresh label combo the count is 0.
	before := testutil.CollectAndCount(operationDuration)

	done := StartOperation("t-op-ctrl", "reconcile_spec")
	time.Sleep(time.Millisecond)
	done()

	after := testutil.CollectAndCount(operationDuration)
	if after <= before {
		t.Errorf("operation_duration_seconds count did not increase: before=%d after=%d", before, after)
	}
}
