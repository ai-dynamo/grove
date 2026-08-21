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

package podgangmigrator

import (
	"context"
	"fmt"
	"time"

	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// MigrationRequeueInterval is how long a reconciler waits before retrying while the owning
// PodCliqueSet is being migrated to the epoch-based PodGang scheme. Migration is quick, so a short
// interval keeps the gated reconcilers responsive once the gate clears.
const MigrationRequeueInterval = 5 * time.Second

// gateWriteBackoff bounds the inline retries of the migration-gate status write. The gate runs once
// at startup before the manager starts, so a few quick retries absorb a transient API server blip
// without forcing a container restart.
var gateWriteBackoff = wait.Backoff{Steps: 5, Duration: 50 * time.Millisecond, Factor: 2.0, Jitter: 0.1}

// SetMigrationGateForLegacyPodCliqueSets sets the PodGangMigrationInProgress condition on every
// PodCliqueSet that must still be migrated from the legacy PodGang naming to the epoch-based scheme.
// It runs before the controller manager starts, so the gate is already closed on every legacy
// PodCliqueSet before the PodCliqueScalingGroup and PodClique reconcilers receive their first event.
// It writes only the status condition, mutates no PodGang / PodClique / Pod, and is idempotent.
//
// Because this runs before any PodGangMap can be created this run, the presence of a PodGangMap for a
// PodCliqueSet means a prior run already migrated it (the migration component clears the condition only
// when the whole PodCliqueSet is migrated). So per PodCliqueSet: skip zero-replica PodCliqueSets, skip
// those already carrying the condition, skip those that already have a PodGangMap, and set the
// condition on the rest.
func SetMigrationGateForLegacyPodCliqueSets(ctx context.Context, cl client.Client, logger logr.Logger) error {
	pcsList := &grovecorev1alpha1.PodCliqueSetList{}
	if err := cl.List(ctx, pcsList); err != nil {
		return fmt.Errorf("failed to list PodCliqueSets: %w", err)
	}
	for i := range pcsList.Items {
		pcs := &pcsList.Items[i]
		// A zero-replica PodCliqueSet has no PodGangs/PodCliques/Pods and no PodGangMap. When it scales
		// up the PodGangMap component creates the PodGangMap and PodGangs are named with the new scheme
		// directly, so there is nothing to migrate and nothing to gate.
		if pcs.Spec.Replicas == 0 {
			logger.V(4).Info("Skipping PodGang migration gate for zero-replica PodCliqueSet", "podCliqueSet", client.ObjectKeyFromObject(pcs))
			continue
		}
		if meta.IsStatusConditionTrue(pcs.Status.Conditions, apiconstants.ConditionTypePodGangMigrationInProgress) {
			logger.V(4).Info("PodGang migration gate already set on PodCliqueSet", "podCliqueSet", client.ObjectKeyFromObject(pcs))
			continue
		}
		pgms, err := componentutils.ListPodGangMapsForPCS(ctx, cl, client.ObjectKeyFromObject(pcs))
		if err != nil {
			return fmt.Errorf("failed to list PodGangMaps for PodCliqueSet %v: %w", client.ObjectKeyFromObject(pcs), err)
		}
		// A PodGangMap exists only because a prior run created it after this gate-setter ran, and the
		// migration component clears the condition only once the whole PodCliqueSet is migrated.
		// So a PodGangMap present with the condition absent means migration is already complete.
		if len(pgms) > 0 {
			logger.V(4).Info("Skipping PodGang migration gate for already-migrated PodCliqueSet, PodGangMap present", "podCliqueSet", client.ObjectKeyFromObject(pcs))
			continue
		}
		pcsObjectKey := client.ObjectKeyFromObject(pcs)
		if err := retry.OnError(gateWriteBackoff, k8sutils.IsRetriableAPIError, func() error {
			latest := &grovecorev1alpha1.PodCliqueSet{}
			if err := cl.Get(ctx, pcsObjectKey, latest); err != nil {
				return err
			}
			meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
				Type:    apiconstants.ConditionTypePodGangMigrationInProgress,
				Status:  metav1.ConditionTrue,
				Reason:  "LegacyPodGangsPendingMigration",
				Message: "PodCliqueSet predates the epoch-based PodGang scheme and is pending migration",
			})
			return cl.Status().Update(ctx, latest)
		}); err != nil {
			return fmt.Errorf("failed to set PodGang migration gate on PodCliqueSet %v: %w", pcsObjectKey, err)
		}
		logger.Info("Set PodGang migration gate on legacy PodCliqueSet", "podCliqueSet", pcsObjectKey)
	}
	return nil
}

// IsMigrationInProgress reports whether the PodCliqueSet is being migrated to the epoch-based PodGang
// scheme, i.e. its PodGangMigrationInProgress condition is True. While migration is in progress the
// PodCliqueScalingGroup and PodClique reconcilers must not act on spec changes, so scaling does not
// interleave with the migration.
func IsMigrationInProgress(pcs *grovecorev1alpha1.PodCliqueSet) bool {
	return meta.IsStatusConditionTrue(pcs.Status.Conditions, apiconstants.ConditionTypePodGangMigrationInProgress)
}
