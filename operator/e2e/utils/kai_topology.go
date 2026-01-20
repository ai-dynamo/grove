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

//go:build e2e

package utils

import (
	"fmt"

	kaischedulingv2alpha2 "github.com/NVIDIA/KAI-scheduler/pkg/apis/scheduling/v2alpha2"

	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/dynamic"
)

// ExpectedSubGroup defines the expected structure of a KAI PodGroup SubGroup for verification
type ExpectedSubGroup struct {
	Name                   string
	MinMember              int32
	Parent                 *string
	RequiredTopologyLevel  string
	PreferredTopologyLevel string
}

// GetKAIPodGroupsForPCS retrieves all KAI PodGroups for a given PodCliqueSet by label selector
// KAI scheduler creates PodGroups with label: app.kubernetes.io/part-of=<pcs-name>
// Returns a list of PodGroups that tests can filter by owner reference if needed
func GetKAIPodGroupsForPCS(ctx context.Context, dynamicClient dynamic.Interface, namespace, pcsName string) ([]kaischedulingv2alpha2.PodGroup, error) {
	// List PodGroups using label selector
	labelSelector := fmt.Sprintf("app.kubernetes.io/part-of=%s", pcsName)
	unstructuredList, err := dynamicClient.Resource(kaiPodGroupGVR).Namespace(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list KAI PodGroups with label %s in namespace %s: %w", labelSelector, namespace, err)
	}

	// Convert all items to typed PodGroups
	podGroups := make([]kaischedulingv2alpha2.PodGroup, 0, len(unstructuredList.Items))
	for _, item := range unstructuredList.Items {
		var podGroup kaischedulingv2alpha2.PodGroup
		if err := ConvertUnstructuredToTyped(item.Object, &podGroup); err != nil {
			return nil, fmt.Errorf("failed to convert KAI PodGroup to typed: %w", err)
		}
		podGroups = append(podGroups, podGroup)
	}

	if len(podGroups) == 0 {
		return nil, fmt.Errorf("no KAI PodGroups found for PCS %s in namespace %s", pcsName, namespace)
	}

	return podGroups, nil
}

// WaitForKAIPodGroups waits for KAI PodGroups for the given PCS to exist and returns them
func WaitForKAIPodGroups(ctx context.Context, dynamicClient dynamic.Interface, namespace, pcsName string, timeout, interval time.Duration, logger *Logger) ([]kaischedulingv2alpha2.PodGroup, error) {
	var podGroups []kaischedulingv2alpha2.PodGroup
	err := PollForCondition(ctx, timeout, interval, func() (bool, error) {
		pgs, err := GetKAIPodGroupsForPCS(ctx, dynamicClient, namespace, pcsName)
		if err != nil {
			logger.Debugf("Waiting for KAI PodGroups for PCS %s/%s: %v", namespace, pcsName, err)
			return false, nil
		}
		podGroups = pgs
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("timed out waiting for KAI PodGroups for PCS %s/%s: %w", namespace, pcsName, err)
	}
	return podGroups, nil
}

// FilterPodGroupByOwner filters a list of PodGroups to find the one owned by the specified PodGang
func FilterPodGroupByOwner(podGroups []kaischedulingv2alpha2.PodGroup, podGangName string) (*kaischedulingv2alpha2.PodGroup, error) {
	for i := range podGroups {
		for _, ref := range podGroups[i].OwnerReferences {
			if ref.Kind == "PodGang" && ref.Name == podGangName {
				return &podGroups[i], nil
			}
		}
	}
	return nil, fmt.Errorf("no PodGroup found owned by PodGang %s", podGangName)
}

// VerifyKAIPodGroupTopologyConstraint verifies the top-level TopologyConstraint of a KAI PodGroup
func VerifyKAIPodGroupTopologyConstraint(podGroup *kaischedulingv2alpha2.PodGroup, expectedRequired, expectedPreferred string, logger *Logger) error {
	actualRequired := podGroup.Spec.TopologyConstraint.RequiredTopologyLevel
	actualPreferred := podGroup.Spec.TopologyConstraint.PreferredTopologyLevel

	if actualRequired != expectedRequired {
		return fmt.Errorf("KAI PodGroup %s top-level RequiredTopologyLevel: got %q, expected %q",
			podGroup.Name, actualRequired, expectedRequired)
	}

	if actualPreferred != expectedPreferred {
		return fmt.Errorf("KAI PodGroup %s top-level PreferredTopologyLevel: got %q, expected %q",
			podGroup.Name, actualPreferred, expectedPreferred)
	}

	logger.Infof("KAI PodGroup %s top-level TopologyConstraint verified: required=%q, preferred=%q",
		podGroup.Name, actualRequired, actualPreferred)
	return nil
}

// VerifyKAIPodGroupSubGroups verifies the SubGroups of a KAI PodGroup
func VerifyKAIPodGroupSubGroups(podGroup *kaischedulingv2alpha2.PodGroup, expectedSubGroups []ExpectedSubGroup, logger *Logger) error {
	if len(podGroup.Spec.SubGroups) != len(expectedSubGroups) {
		return fmt.Errorf("KAI PodGroup %s has %d SubGroups, expected %d",
			podGroup.Name, len(podGroup.Spec.SubGroups), len(expectedSubGroups))
	}

	// Build a map of actual SubGroups by name for easier lookup
	actualSubGroups := make(map[string]kaischedulingv2alpha2.SubGroup)
	for _, sg := range podGroup.Spec.SubGroups {
		actualSubGroups[sg.Name] = sg
	}

	for _, expected := range expectedSubGroups {
		actual, ok := actualSubGroups[expected.Name]
		if !ok {
			return fmt.Errorf("KAI PodGroup %s missing expected SubGroup %q", podGroup.Name, expected.Name)
		}

		// Verify Parent
		if expected.Parent == nil && actual.Parent != nil {
			return fmt.Errorf("SubGroup %q Parent: got %q, expected nil", expected.Name, *actual.Parent)
		}
		if expected.Parent != nil && actual.Parent == nil {
			return fmt.Errorf("SubGroup %q Parent: got nil, expected %q", expected.Name, *expected.Parent)
		}
		if expected.Parent != nil && actual.Parent != nil && *expected.Parent != *actual.Parent {
			return fmt.Errorf("SubGroup %q Parent: got %q, expected %q", expected.Name, *actual.Parent, *expected.Parent)
		}

		// Verify MinMember
		if actual.MinMember != expected.MinMember {
			return fmt.Errorf("SubGroup %q MinMember: got %d, expected %d", expected.Name, actual.MinMember, expected.MinMember)
		}

		// Verify TopologyConstraint
		actualRequired := ""
		actualPreferred := ""
		if actual.TopologyConstraint != nil {
			actualRequired = actual.TopologyConstraint.RequiredTopologyLevel
			actualPreferred = actual.TopologyConstraint.PreferredTopologyLevel
		}

		if actualRequired != expected.RequiredTopologyLevel {
			return fmt.Errorf("SubGroup %q RequiredTopologyLevel: got %q, expected %q",
				expected.Name, actualRequired, expected.RequiredTopologyLevel)
		}
		if actualPreferred != expected.PreferredTopologyLevel {
			return fmt.Errorf("SubGroup %q PreferredTopologyLevel: got %q, expected %q",
				expected.Name, actualPreferred, expected.PreferredTopologyLevel)
		}

		logger.Debugf("SubGroup %q verified: parent=%v, minMember=%d, required=%q, preferred=%q",
			expected.Name, actual.Parent, actual.MinMember, actualRequired, actualPreferred)
	}

	logger.Infof("KAI PodGroup %s verified with %d SubGroups", podGroup.Name, len(expectedSubGroups))
	return nil
}
