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
//
// Existing controller-runtime built-ins (controller_runtime_reconcile_total,
// controller_runtime_reconcile_time_seconds, controller_runtime_active_workers)
// cover reconcile count, duration, and in-flight tracking. Only signals absent
// from those built-ins are added here.
package metrics

import (
	"errors"
	"net"

	"github.com/prometheus/client_golang/prometheus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

const namespace = "grove_operator"

var (
	// statusUpdateConflictTotal counts 409 Conflict errors on Status Update/Patch calls,
	// partitioned by controller and the kind of resource being updated.
	// controller_runtime_reconcile_total cannot attribute conflicts to a specific status-update
	// target kind, so this metric fills that gap.
	statusUpdateConflictTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "status_update_conflict_total",
			Help:      "Total 409 Conflict errors on status Update/Patch per controller, partitioned by target resource kind.",
		},
		[]string{"controller", "target_kind"},
	)

	// reconcileErrorsTotal counts reconcile errors partitioned by controller and error type,
	// enabling transient vs. persistent error breakdown that controller_runtime_reconcile_total
	// does not provide.
	reconcileErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "reconcile_errors_total",
			Help:      "Total reconcile errors per controller, partitioned by error type (conflict, not_found, invalid, transient, server_error, unknown).",
		},
		[]string{"controller", "error_type"},
	)
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		statusUpdateConflictTotal,
		reconcileErrorsTotal,
	)
}

// RecordStatusUpdateConflict records a 409 Conflict on a status Update or Patch call.
// targetKind is the Kind of the resource being updated (e.g. "PodCliqueSet").
func RecordStatusUpdateConflict(controller, targetKind string) {
	statusUpdateConflictTotal.WithLabelValues(controller, targetKind).Inc()
}

// RecordReconcileError categorises err and increments reconcile_errors_total.
// It is a no-op when err is nil.
func RecordReconcileError(controller string, err error) {
	if err == nil {
		return
	}
	reconcileErrorsTotal.WithLabelValues(controller, errorType(err)).Inc()
}

func errorType(err error) string {
	switch {
	case apierrors.IsConflict(err):
		return "conflict"
	case apierrors.IsNotFound(err):
		return "not_found"
	case apierrors.IsInvalid(err):
		return "invalid"
	case apierrors.IsServerTimeout(err) || apierrors.IsTimeout(err) || apierrors.IsTooManyRequests(err):
		return "transient"
	case apierrors.IsInternalError(err) || apierrors.IsServiceUnavailable(err):
		return "server_error"
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return "transient"
	}
	return "unknown"
}
