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

// Package metrics provides Prometheus metrics for Grove operator controllers.
// All metrics use the grove_operator namespace prefix and are registered on
// the controller-runtime global registry so they are served on the existing
// metrics endpoint without additional configuration.
package metrics

import (
	"context"
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const namespace = "grove_operator"

var (
	reconcileTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "reconcile_total",
			Help:      "Total number of reconcile operations per controller, partitioned by result (success, error, requeue, requeue_after).",
		},
		[]string{"controller", "result"},
	)

	reconcileDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "reconcile_duration_seconds",
			Help:      "Duration of each reconcile operation in seconds, per controller.",
			Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0, 60.0},
		},
		[]string{"controller"},
	)

	inFlightReconciles = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "in_flight_reconciles",
			Help:      "Number of currently executing reconcile operations, per controller.",
		},
		[]string{"controller"},
	)

	operationDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "operation_duration_seconds",
			Help:      "Duration of named sub-operations within a reconcile loop (e.g. reconcile_spec, reconcile_status), per controller.",
			Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0},
		},
		[]string{"controller", "operation"},
	)

	conflictTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "conflict_total",
			Help:      "Total number of 409 Conflict errors encountered per controller. High rates indicate concurrent modification contention.",
		},
		[]string{"controller"},
	)
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		reconcileTotal,
		reconcileDuration,
		inFlightReconciles,
		operationDuration,
		conflictTotal,
	)
}

// ObservedReconciler wraps a reconcile.Reconciler with automatic Prometheus instrumentation.
// It records reconcile duration, in-flight count, result totals, and 409 Conflict errors.
type ObservedReconciler struct {
	controller string
	inner      reconcile.Reconciler
}

// NewObservedReconciler returns an ObservedReconciler that instruments the given reconciler
// under the provided controller name label.
func NewObservedReconciler(controller string, inner reconcile.Reconciler) *ObservedReconciler {
	return &ObservedReconciler{controller: controller, inner: inner}
}

// Reconcile implements reconcile.Reconciler.
func (o *ObservedReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	start := time.Now()
	inFlightReconciles.WithLabelValues(o.controller).Inc()
	defer inFlightReconciles.WithLabelValues(o.controller).Dec()

	result, err := o.inner.Reconcile(ctx, req)

	reconcileDuration.WithLabelValues(o.controller).Observe(time.Since(start).Seconds())
	reconcileTotal.WithLabelValues(o.controller, resultLabel(result, err)).Inc()

	if err != nil && isConflict(err) {
		conflictTotal.WithLabelValues(o.controller).Inc()
	}

	return result, err
}

// StartOperation begins timing a named sub-operation and returns a done function
// that must be called when the operation completes to record the elapsed duration.
//
//	done := metrics.StartOperation(controllerName, "reconcile_spec")
//	result := r.reconcileSpec(ctx, logger, obj)
//	done()
func StartOperation(controller, operation string) func() {
	start := time.Now()
	return func() {
		operationDuration.WithLabelValues(controller, operation).Observe(time.Since(start).Seconds())
	}
}

func resultLabel(result ctrl.Result, err error) string {
	if err != nil {
		return "error"
	}
	if result.RequeueAfter > 0 {
		return "requeue_after"
	}
	if result.Requeue {
		return "requeue"
	}
	return "success"
}

func isConflict(err error) bool {
	var statusErr *apierrors.StatusError
	if errors.As(err, &statusErr) {
		return apierrors.IsConflict(statusErr)
	}
	return apierrors.IsConflict(err)
}
