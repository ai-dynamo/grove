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

package kubernetes

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// HasConditionChanged checks if a specific condition passed as newCondition has either been set newly
// or it has changed when compared to the existing condition.
func HasConditionChanged(existingConditions []metav1.Condition, newCondition metav1.Condition) bool {
	existingCond := meta.FindStatusCondition(existingConditions, newCondition.Type)
	if existingCond == nil {
		return true
	}
	return existingCond.Status != newCondition.Status ||
		existingCond.Reason != newCondition.Reason ||
		existingCond.Message != newCondition.Message
}

// IsConditionTrue checks for a specific Condition amongst a list of Conditions and returns if the Condition.Status is true otherwise false is returned.
func IsConditionTrue(existingConditions []metav1.Condition, conditionType string) bool {
	condStatus := GetConditionStatus(existingConditions, conditionType)
	if condStatus == nil {
		return false
	}
	return *condStatus == metav1.ConditionTrue
}

// IsConditionUnknown checks for a specific Condition amongst a list of Conditions and returns if the Condition.Status is Unknown otherwise false is returned.
func IsConditionUnknown(existingConditions []metav1.Condition, conditionType string) bool {
	condStatus := GetConditionStatus(existingConditions, conditionType)
	if condStatus == nil {
		return false
	}
	return *condStatus == metav1.ConditionUnknown
}

// GetConditionStatus looks up the conditionType from a slice of conditions. If the condition is found then it returns
// the status else it will return nil.
func GetConditionStatus(conditions []metav1.Condition, conditionType string) *metav1.ConditionStatus {
	foundCond := meta.FindStatusCondition(conditions, conditionType)
	if foundCond == nil {
		return nil
	}
	return &foundCond.Status
}

// HasCondition checks if the condition of type conditionType is already present in the existing slice of conditions.
// Returns true if it does else it returns false.
func HasCondition(existingConditions []metav1.Condition, conditionType string) bool {
	foundInitializedCond := meta.FindStatusCondition(existingConditions, conditionType)
	return foundInitializedCond != nil
}
