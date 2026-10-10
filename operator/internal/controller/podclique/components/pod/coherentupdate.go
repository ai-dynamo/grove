// Copyright 2026 The Grove Authors.
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

package pod

import (
	"context"

	"github.com/go-logr/logr"
)

// markCoherentUpdateEndIfConverged marks the end of a standalone PodClique's coherent update once every
// pod has reached the current revision. Under a coherent update the pods are rolled by the PodGangMap
// count-driven distribution rather than by processPendingUpdates, so nothing else calls
// markRollingUpdateEnd. Without this the PodClique status never advances CurrentPodTemplateHash and the
// PodCliqueSet orchestrator never observes the replica as complete. Readiness is not checked here because
// the PodCliqueSet orchestrator gates replica completion on readiness separately.
func (r _resource) markCoherentUpdateEndIfConverged(ctx context.Context, logger logr.Logger, ss *syncSnapshot) error {
	if ss.pclq.Status.Replicas != ss.pclq.Spec.Replicas {
		return nil
	}
	if ss.pclq.Status.UpdatedReplicas != ss.pclq.Status.Replicas {
		return nil
	}
	return r.markRollingUpdateEnd(ctx, logger, ss.pclq)
}
