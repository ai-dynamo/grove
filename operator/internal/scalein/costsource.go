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

// Package scalein holds the scale-in cost source: the one place that knows where a pod's scale-in cost
// is carried. Selection code consumes the cost as a number and never sees the carrier.
package scalein

import (
	"fmt"
	"strconv"

	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"

	corev1 "k8s.io/api/core/v1"
)

// CostSource yields a pod's scale-in cost from whatever carries it. Lower costs are removed first.
// A pod the source reports as absent costs 0.
type CostSource interface {
	// CostOf returns the pod's scale-in cost and whether the pod carries one.
	CostOf(pod *corev1.Pod) (cost int32, present bool)
}

// NewCostSource returns the CostSource selected by the operator configuration.
func NewCostSource(name configv1beta1.ScaleInCostSourceName) (CostSource, error) {
	switch name {
	case "", configv1beta1.ScaleInCostSourceNone:
		return noCostSource{}, nil
	case configv1beta1.ScaleInCostSourcePodDeletionCost:
		return podDeletionCostSource{}, nil
	default:
		return nil, fmt.Errorf("unsupported scale-in cost source %q, supported sources: %v", name, configv1beta1.AllScaleInCostSourceNames)
	}
}

// noCostSource reads no cost. Every pod is absent, so selection is the order Grove produces without a cost.
type noCostSource struct{}

// CostOf reports every pod as absent.
func (noCostSource) CostOf(*corev1.Pod) (int32, bool) {
	return 0, false
}

// podDeletionCostSource reads the controller.kubernetes.io/pod-deletion-cost annotation, the key a
// ReplicaSet reads. The apiserver validates the value as an int32 while the PodDeletionCost feature
// gate is on, so a value that does not parse is treated as absent.
type podDeletionCostSource struct{}

// CostOf returns the annotation's value, or absent when the annotation is missing or malformed. The value is
// parsed the way the ReplicaSet controller parses it: a leading plus sign or a leading zero, which the apiserver
// rejects while the feature gate is on, makes the value malformed rather than a cost.
func (podDeletionCostSource) CostOf(pod *corev1.Pod) (int32, bool) {
	raw, ok := pod.Annotations[corev1.PodDeletionCost]
	if !ok || !hasValidFirstDigit(raw) {
		return 0, false
	}
	cost, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, false
	}
	return int32(cost), true
}

// hasValidFirstDigit reports whether the value starts the way a pod-deletion-cost may: a minus sign, a non-zero
// digit, or the single digit 0. "+5" and "007" are rejected, as the ReplicaSet controller rejects them.
func hasValidFirstDigit(value string) bool {
	if len(value) == 0 {
		return false
	}
	return value[0] == '-' || (value[0] == '0' && value == "0") || (value[0] >= '1' && value[0] <= '9')
}

// ReadsNoCost reports whether the source reads no cost at all: a nil source or the source selected by the
// configuration value none. A caller that would list pods only to cost them can skip the list for such a source,
// so a scale-in with the feature off does the work it did before the feature existed.
func ReadsNoCost(source CostSource) bool {
	if source == nil {
		return true
	}
	_, isNone := source.(noCostSource)
	return isNone
}

// CostOf returns the cost source reports for the pod, 0 when the pod carries none. A nil source reads no cost,
// so a component built without one orders its scale-ins as if the source were none.
func CostOf(source CostSource, pod *corev1.Pod) int32 {
	if source == nil {
		return 0
	}
	cost, _ := source.CostOf(pod)
	return cost
}

// MinCost returns the lowest cost over the pods, with a pod that carries no cost counting as 0, and 0 for no
// pods. It aggregates a replica's pods into the replica's cost: one marked pod speaks for its gang.
func MinCost(source CostSource, pods []*corev1.Pod) int32 {
	var minCost int32
	for i, pod := range pods {
		cost := CostOf(source, pod)
		if i == 0 || cost < minCost {
			minCost = cost
		}
	}
	return minCost
}
