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

package clustertopology

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DefaultSyncRetryBackoff is the retry backoff used by SynchronizeTopologyWithRetry.
// Six steps with doubling delay gives roughly 60 seconds of retry budget (1+2+4+8+16+32).
var DefaultSyncRetryBackoff = wait.Backoff{
	Duration: time.Second,
	Factor:   2.0,
	Jitter:   0.1,
	Steps:    6,
}

// SynchronizeTopologyWithRetry wraps SynchronizeTopology with a bounded exponential
// backoff so that transient API-server errors (5xx, timeouts, service-unavailable)
// at operator startup do not crash the operator process.
// Permanent errors (Forbidden, Unauthorized) are returned immediately without retrying.
func SynchronizeTopologyWithRetry(ctx context.Context, cl client.Client, logger logr.Logger, backends map[string]scheduler.TopologyAwareBackend, backoff wait.Backoff) error {
	attempt := 0
	return retry.OnError(backoff, isTransientAPIError, func() error {
		if attempt > 0 {
			logger.Info("Retrying topology synchronization after transient error", "attempt", attempt+1)
		}
		attempt++
		return SynchronizeTopology(ctx, cl, logger, backends)
	})
}

// isTransientAPIError reports whether err is a transient error that is safe to retry.
// It covers both Kubernetes API-level errors (5xx, timeouts) and transport-level
// errors (connection refused, EOF, net timeout) that surface before a StatusError
// is returned — e.g. during an API-server rolling restart or network partition.
// Permanent errors (Forbidden, Unauthorized) return false.
func isTransientAPIError(err error) bool {
	if apierrors.IsServerTimeout(err) ||
		apierrors.IsServiceUnavailable(err) ||
		apierrors.IsInternalError(err) ||
		apierrors.IsTimeout(err) ||
		apierrors.IsTooManyRequests(err) {
		return true
	}
	// Transport-level transient errors.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNREFUSED)
}

// SynchronizeTopology synchronizes scheduler-specific topology resources at operator startup.
// Lists all existing ClusterTopologyBinding resources and ensures backend topologies exist for each.
// Called before controllers start to avoid races with PCS reconciliation.
func SynchronizeTopology(ctx context.Context, cl client.Client, logger logr.Logger, backends map[string]scheduler.TopologyAwareBackend) error {
	ctList := &grovecorev1alpha1.ClusterTopologyBindingList{}
	if err := cl.List(ctx, ctList); err != nil {
		return fmt.Errorf("failed to list ClusterTopologyBinding resources: %w", err)
	}
	for i := range ctList.Items {
		ct := &ctList.Items[i]
		schedulerRefMap := BuildSchedulerReferenceMap(ct.Spec.SchedulerTopologyBindings)

		for backendName, tasBackend := range backends {
			// Only sync grove-managed scheduler topology resources (not listed in schedulerTopologyReferences).
			// Externally-managed scheduler topology resources are handled by the ClusterTopologyBinding controller via CheckTopologyDrift.
			if _, isExternallyManaged := schedulerRefMap[backendName]; isExternallyManaged {
				continue
			}
			if err := tasBackend.SyncTopology(ctx, cl, ct); err != nil {
				return fmt.Errorf("failed to sync topology %s for backend %s: %w", ct.Name, backendName, err)
			}
		}
		logger.Info("Synchronized backend topologies for ClusterTopologyBinding", "name", ct.Name)
	}
	return nil
}

// GetClusterTopologyLevels retrieves the TopologyLevels from the specified ClusterTopologyBinding resource.
func GetClusterTopologyLevels(ctx context.Context, cl client.Client, name string) ([]grovecorev1alpha1.TopologyLevel, error) {
	clusterTopology := &grovecorev1alpha1.ClusterTopologyBinding{}
	if err := cl.Get(ctx, client.ObjectKey{Name: name}, clusterTopology); err != nil {
		return nil, err
	}
	return clusterTopology.Spec.Levels, nil
}

// BuildSchedulerReferenceMap builds a map from scheduler name to SchedulerTopologyReference pointer.
func BuildSchedulerReferenceMap(refs []grovecorev1alpha1.SchedulerTopologyBinding) map[string]*grovecorev1alpha1.SchedulerTopologyBinding {
	m := make(map[string]*grovecorev1alpha1.SchedulerTopologyBinding, len(refs))
	for i := range refs {
		m[refs[i].SchedulerName] = &refs[i]
	}
	return m
}
