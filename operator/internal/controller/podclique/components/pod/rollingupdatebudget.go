// /*
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
// */

package pod

import (
	"fmt"
	"math"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

type rollingUpdateBudgetBlockReason string

const rollingUpdateBudgetMaxUnavailableReached rollingUpdateBudgetBlockReason = "MaxUnavailableReached"

type rollingUpdateBudget struct {
	enabled     bool
	allowed     int
	unavailable int
	limit       int
	reason      rollingUpdateBudgetBlockReason
}

func (b rollingUpdateBudget) blocked() bool {
	return b.enabled && b.allowed == 0
}

func (r _resource) getRollingUpdateBudget(sc *syncContext) (rollingUpdateBudget, error) {
	return evaluateRollingUpdateBudget(
		sc.pclq,
		sc.existingPCLQPods,
		r.expectationsStore.GetDeleteExpectations(sc.pclqExpectationsStoreKey),
		r.expectationsStore.GetCreateExpectations(sc.pclqExpectationsStoreKey),
	)
}

func evaluateRollingUpdateBudget(
	pclq *grovecorev1alpha1.PodClique,
	pods []*corev1.Pod,
	deleteExpectations []types.UID,
	createExpectations []types.UID,
) (rollingUpdateBudget, error) {
	strategy := pclq.Spec.RollingUpdate
	if strategy == nil || strategy.MaxUnavailable == nil {
		return rollingUpdateBudget{allowed: math.MaxInt}, nil
	}
	if pclq.Spec.Replicas <= 0 {
		return rollingUpdateBudget{}, fmt.Errorf("replicas for PodClique must be positive, got %d", pclq.Spec.Replicas)
	}

	deleteExpectationSet := make(map[types.UID]struct{}, len(deleteExpectations))
	for _, uid := range deleteExpectations {
		deleteExpectationSet[uid] = struct{}{}
	}

	available := 0
	for _, pod := range pods {
		_, deletionExpected := deleteExpectationSet[pod.UID]
		if pod.Status.Phase == corev1.PodRunning &&
			k8sutils.IsPodReady(pod) &&
			!k8sutils.IsResourceTerminating(pod.ObjectMeta) &&
			!deletionExpected {
			available++
		}
	}

	desired := int(pclq.Spec.Replicas)
	availabilityUnavailable := max(0, desired-min(available, desired))
	// Surplus Pods being removed during scale-in are outside the new desired
	// replica count. Count all in-flight Pod lifecycle changes as well so
	// scale-in and rolling-update deletions consume the same budget.
	activeChanges := countActivePodChanges(pods, deleteExpectations, createExpectations)
	unavailable := max(availabilityUnavailable, activeChanges)
	budget := rollingUpdateBudget{
		enabled:     true,
		allowed:     math.MaxInt,
		unavailable: unavailable,
	}

	budget.limit = min(int(*strategy.MaxUnavailable), desired)
	if budget.unavailable >= budget.limit {
		budget.allowed = 0
		budget.reason = rollingUpdateBudgetMaxUnavailableReached
		return budget, nil
	}
	budget.allowed = budget.limit - budget.unavailable
	return budget, nil
}

func countActivePodChanges(
	pods []*corev1.Pod,
	deleteExpectations []types.UID,
	createExpectations []types.UID,
) int {
	deleteExpectationSet := make(map[types.UID]struct{}, len(deleteExpectations))
	for _, uid := range deleteExpectations {
		deleteExpectationSet[uid] = struct{}{}
	}

	activeKeys := make(map[string]struct{})
	observedUIDs := make(map[types.UID]struct{}, len(pods))
	for _, pod := range pods {
		observedUIDs[pod.UID] = struct{}{}
		_, deletionExpected := deleteExpectationSet[pod.UID]
		if deletionExpected || isPodActivelyChanging(pod) {
			activeKeys[getPodChangeKey(pod)] = struct{}{}
		}
	}

	// Expectations can be recorded before the informer cache observes the
	// corresponding object state. Reserve a slot until the cache converges.
	for _, uid := range deleteExpectations {
		if _, observed := observedUIDs[uid]; !observed {
			activeKeys["delete/"+string(uid)] = struct{}{}
		}
	}
	for _, uid := range createExpectations {
		if _, observed := observedUIDs[uid]; !observed {
			activeKeys["create/"+string(uid)] = struct{}{}
		}
	}
	return len(activeKeys)
}

func getPodChangeKey(pod *corev1.Pod) string {
	if podIndex, ok := pod.Labels[apicommon.LabelPodCliquePodIndex]; ok {
		return "index/" + podIndex
	}
	if pod.UID != "" {
		return "uid/" + string(pod.UID)
	}
	return "name/" + pod.Name
}

func isPodActivelyChanging(pod *corev1.Pod) bool {
	return k8sutils.IsResourceTerminating(pod.ObjectMeta) ||
		pod.Status.Phase != corev1.PodRunning ||
		!k8sutils.IsPodReady(pod)
}

func isPodChangeConcurrencyControlEnabled(pclq *grovecorev1alpha1.PodClique) bool {
	return pclq.Spec.RollingUpdate != nil &&
		pclq.Spec.RollingUpdate.MaxUnavailable != nil
}

func (r _resource) getScaleInBudget(sc *syncContext) (rollingUpdateBudget, error) {
	pclq := sc.pclq.DeepCopy()
	// Evaluate scale-in against the current population rather than the reduced
	// desired count. This keeps a surplus Pod charged until its deletion has
	// disappeared from the informer cache.
	pclq.Spec.Replicas = int32(
		len(sc.existingPCLQPods) +
			len(r.expectationsStore.GetCreateExpectations(sc.pclqExpectationsStoreKey)),
	)
	return evaluateRollingUpdateBudget(
		pclq,
		sc.existingPCLQPods,
		r.expectationsStore.GetDeleteExpectations(sc.pclqExpectationsStoreKey),
		r.expectationsStore.GetCreateExpectations(sc.pclqExpectationsStoreKey),
	)
}
