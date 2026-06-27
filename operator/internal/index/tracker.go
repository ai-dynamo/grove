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

package index

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// GetAvailableIndices returns the `requiredIndicesCount` available indices for pods.
// It considers as used: indices from active existing pods (by hostname) and any reservedIndices
// (e.g. indices assigned to in-flight creations not yet observed in the pod list).
// Available indices are filled from lowest to highest (starting from 0).
// Active pods are those that are not terminating, not permanently failed (RestartPolicy=Never), and not succeeded.
// reservedIndices can be nil; it is used to reserve indices that are already assigned but not yet visible in existingPods.
func GetAvailableIndices(logger logr.Logger, existingPods []*corev1.Pod, reservedIndices sets.Set[int], requiredIndicesCount int) ([]int, error) {
	usedIndices, err := extractUsedIndices(logger, existingPods)
	if err != nil {
		return nil, err
	}
	if len(reservedIndices) > 0 {
		usedIndices = usedIndices.Union(reservedIndices)
	}
	return findAvailableIndices(&usedIndices, requiredIndicesCount), nil
}

// extractUsedIndices extracts and validates indices from existing pods.
func extractUsedIndices(logger logr.Logger, existingPods []*corev1.Pod) (sets.Set[int], error) {
	usedIndices := sets.New[int]()

	for _, pod := range existingPods {
		// Only consider active pods as occupying their indices
		if !k8sutils.IsPodActive(pod) {
			logger.Info("hostname available due to inactive pod", "pod", pod.Name, "hostname", pod.Spec.Hostname)
			continue
		}

		index, err := extractIndexFromHostname(pod.Spec.Hostname)
		if err != nil {
			return usedIndices, fmt.Errorf("failed to extract index from hostname for pod %s with hostname %s: %w", pod.Name, pod.Spec.Hostname, err)
		}

		if usedIndices.Has(index) {
			return usedIndices, fmt.Errorf("duplicate index %d found for pod %s with hostname %s", index, pod.Name, pod.Spec.Hostname)
		}

		usedIndices.Insert(index)
	}

	return usedIndices, nil
}

// findAvailableIndices finds the next count available indices starting from 0.
func findAvailableIndices(usedIndices *sets.Set[int], requiredIndicesCount int) []int {
	availableIndices := make([]int, 0, requiredIndicesCount)
	currentIndex := 0
	for requiredIndicesCount > 0 {
		if !usedIndices.Has(currentIndex) {
			availableIndices = append(availableIndices, currentIndex)
			requiredIndicesCount--
		}
		currentIndex++
	}
	return availableIndices
}

// extractIndexFromHostname extracts the numeric index from a pod hostname.
func extractIndexFromHostname(hostname string) (int, error) {
	if hostname == "" {
		return -1, errors.New("hostname is empty")
	}

	parts := strings.Split(hostname, "-")

	if len(parts) < 2 {
		return -1, errors.New("hostname does not contain index suffix")
	}

	indexStr := parts[len(parts)-1]
	index, err := strconv.Atoi(indexStr)
	if err != nil {
		return -1, fmt.Errorf("failed to parse index from hostname suffix '%s': %w", indexStr, err)
	}

	if index < 0 {
		return -1, fmt.Errorf("extracted index %d is negative", index)
	}

	return index, nil
}
