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

package expectations

import (
	"fmt"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/expect"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
)

// PCSGScopedExpectationsStoreKey returns the key under which a PodCliqueScalingGroup's in-flight
// member PodClique delete expectations are recorded. The key has the form "<namespace>/<pcsg-name>"
func PCSGScopedExpectationsStoreKey(pcsgObjectMeta metav1.ObjectMeta) (string, error) {
	key, err := cache.MetaNamespaceKeyFunc(&grovecorev1alpha1.PodCliqueScalingGroup{ObjectMeta: pcsgObjectMeta})
	if err != nil {
		return "", fmt.Errorf("error getting key for object %s/%s: %v", pcsgObjectMeta.Namespace, pcsgObjectMeta.Name, err)
	}
	return key, nil
}

// ClearPCSGExpectations removes the PodCliqueScalingGroup's delete-expectations entry from the store.
// It is called on PodCliqueScalingGroup deletion so the store does not retain an entry after the
// object and its member PodCliques are gone.
func ClearPCSGExpectations(logger logr.Logger, store *expect.ExpectationsStore, pcsgObjectMeta metav1.ObjectMeta) error {
	key, err := PCSGScopedExpectationsStoreKey(pcsgObjectMeta)
	if err != nil {
		return err
	}
	return store.DeleteExpectations(logger, key)
}

// PCSGScopedExpectationsStoreKeyForMemberPodClique returns the owning PodCliqueScalingGroup's
// expectations key for a member PodClique, derived from its namespace and its
// grove.io/podcliquescalinggroup label. The second return is false when the label is absent.
func PCSGScopedExpectationsStoreKeyForMemberPodClique(pclq *grovecorev1alpha1.PodClique) (string, bool) {
	pcsgName, ok := pclq.GetLabels()[apicommon.LabelPodCliqueScalingGroup]
	if !ok || pcsgName == "" {
		return "", false
	}
	return pclq.Namespace + "/" + pcsgName, true
}

// RecordPCSGReplicaDeleteExpectations records delete expectations for the member PodCliques of a
// disrupted PodCliqueScalingGroup replica under the PCSG-scoped key, so the rolling update budget
// counts that replica as unavailable immediately, independent of the eventually-consistent informer
// cache. It is a no-op when the replica has no members.
func RecordPCSGReplicaDeleteExpectations(logger logr.Logger, store *expect.ExpectationsStore, key string, members []grovecorev1alpha1.PodClique) error {
	if len(members) == 0 {
		return nil
	}
	uids := lo.Map(members, func(pclq grovecorev1alpha1.PodClique, _ int) types.UID { return pclq.GetUID() })
	return store.ExpectDeletions(logger, key, uids...)
}

// HasPCSGReplicaDisruptionBeenTriggered reports whether the disruption of a PodCliqueScalingGroup
// replica has already been triggered this rollout, by checking whether any member PodClique carries a
// delete expectation under the PCSG-scoped key. Such a replica is treated as unavailable immediately,
// without waiting for the eventually-consistent cache or member PodClique status to catch up.
func HasPCSGReplicaDisruptionBeenTriggered(store *expect.ExpectationsStore, key string, members []grovecorev1alpha1.PodClique) bool {
	return lo.SomeBy(members, func(pclq grovecorev1alpha1.PodClique) bool {
		return store.HasDeleteExpectation(key, pclq.GetUID())
	})
}

// SyncPCSGReplicaDeleteExpectations reconciles the PodCliqueScalingGroup's delete expectations against
// the observed member PodCliques so an expectation clears once its PodClique is gone.
func SyncPCSGReplicaDeleteExpectations(store *expect.ExpectationsStore, key string, existingPCLQs []grovecorev1alpha1.PodClique) {
	var nonTerminating, terminating []types.UID
	for _, pclq := range existingPCLQs {
		if k8sutils.IsResourceTerminating(pclq.ObjectMeta) {
			terminating = append(terminating, pclq.UID)
		} else {
			nonTerminating = append(nonTerminating, pclq.UID)
		}
	}
	store.SyncExpectations(key, nonTerminating, terminating)
}
