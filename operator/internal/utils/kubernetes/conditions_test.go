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
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestHasConditionsChanged(t *testing.T) {
	testCases := []struct {
		description        string
		existingConditions []metav1.Condition
		newCondition       metav1.Condition
		expectedResult     bool
	}{
		{
			description:        "should return true when condition was never set before",
			existingConditions: []metav1.Condition{},
			newCondition:       metav1.Condition{Type: "Cond1", Status: metav1.ConditionFalse},
			expectedResult:     true,
		},
		{
			description: "should return true when the existing condition has a different status than the new condition",
			existingConditions: []metav1.Condition{
				{Type: "Cond1", Status: metav1.ConditionFalse},
				{Type: "Cond2", Status: metav1.ConditionUnknown},
			},
			newCondition:   metav1.Condition{Type: "Cond1", Status: metav1.ConditionTrue},
			expectedResult: true,
		},
		{
			description: "should return true when existing condition has a different reason than the new condition",
			existingConditions: []metav1.Condition{
				{Type: "Cond1", Status: metav1.ConditionFalse, Reason: "Cond1 reason"},
				{Type: "Cond2", Status: metav1.ConditionUnknown},
			},
			newCondition:   metav1.Condition{Type: "Cond1", Status: metav1.ConditionTrue, Reason: "Cond1 another reason"},
			expectedResult: true,
		},
		{
			description: "should return true when existing condition has a different message than the new condition",
			existingConditions: []metav1.Condition{
				{Type: "Cond1", Status: metav1.ConditionFalse, Message: "Cond1 msg"},
				{Type: "Cond2", Status: metav1.ConditionUnknown},
			},
			newCondition:   metav1.Condition{Type: "Cond1", Status: metav1.ConditionTrue, Message: "Cond1 another msg"},
			expectedResult: true,
		},
		{
			description: "should return false when existing and new condition match",
			existingConditions: []metav1.Condition{
				{Type: "Cond1", Status: metav1.ConditionFalse, Reason: "Cond1 reason", Message: "Cond1 msg"},
				{Type: "Cond2", Status: metav1.ConditionUnknown},
			},
			newCondition:   metav1.Condition{Type: "Cond1", Status: metav1.ConditionFalse, Reason: "Cond1 reason", Message: "Cond1 msg"},
			expectedResult: false,
		},
	}

	t.Parallel()
	for _, testCase := range testCases {
		t.Run(testCase.description, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, testCase.expectedResult, HasConditionChanged(testCase.existingConditions, testCase.newCondition))
		})
	}
}

func TestIsConditionTrue(t *testing.T) {
	testCases := []struct {
		description        string
		existingConditions []metav1.Condition
		conditionType      string
		expectedResult     bool
	}{
		{
			description:        "should return true when condition exists and is True",
			existingConditions: []metav1.Condition{readyTrueCondition(), availableFalseCondition()},
			conditionType:      "Ready",
			expectedResult:     true,
		},
		{
			description:        "should return false when condition exists and is False",
			existingConditions: []metav1.Condition{readyFalseCondition(), availableTrueCondition()},
			conditionType:      "Ready",
			expectedResult:     false,
		},
		{
			description:        "should return false when condition exists and is Unknown",
			existingConditions: []metav1.Condition{readyUnknownCondition(), availableTrueCondition()},
			conditionType:      "Ready",
			expectedResult:     false,
		},
		{
			description:        "should return false when condition does not exist",
			existingConditions: []metav1.Condition{availableTrueCondition()},
			conditionType:      "Ready",
			expectedResult:     false,
		},
		{
			description:        "should return false when conditions list is empty",
			existingConditions: []metav1.Condition{},
			conditionType:      "Ready",
			expectedResult:     false,
		},
		{
			description:        "should return false when conditions list is nil",
			existingConditions: nil,
			conditionType:      "Ready",
			expectedResult:     false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			result := IsConditionTrue(tc.existingConditions, tc.conditionType)
			assert.Equal(t, tc.expectedResult, result)
		})
	}
}

func TestIsConditionUnknown(t *testing.T) {
	testCases := []struct {
		description        string
		existingConditions []metav1.Condition
		conditionType      string
		expectedResult     bool
	}{
		{
			description:        "should return true when condition exists and is Unknown",
			existingConditions: []metav1.Condition{readyUnknownCondition(), availableTrueCondition()},
			conditionType:      "Ready",
			expectedResult:     true,
		},
		{
			description:        "should return false when condition exists and is True",
			existingConditions: []metav1.Condition{readyTrueCondition(), availableUnknownCondition()},
			conditionType:      "Ready",
			expectedResult:     false,
		},
		{
			description:        "should return false when condition exists and is False",
			existingConditions: []metav1.Condition{readyFalseCondition(), availableUnknownCondition()},
			conditionType:      "Ready",
			expectedResult:     false,
		},
		{
			description:        "should return false when condition does not exist",
			existingConditions: []metav1.Condition{availableUnknownCondition()},
			conditionType:      "Ready",
			expectedResult:     false,
		},
		{
			description:        "should return false when conditions list is empty",
			existingConditions: []metav1.Condition{},
			conditionType:      "Ready",
			expectedResult:     false,
		},
		{
			description:        "should return false when conditions list is nil",
			existingConditions: nil,
			conditionType:      "Ready",
			expectedResult:     false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			result := IsConditionUnknown(tc.existingConditions, tc.conditionType)
			assert.Equal(t, tc.expectedResult, result)
		})
	}
}

func TestHasCondition(t *testing.T) {
	testCases := []struct {
		description        string
		existingConditions []metav1.Condition
		conditionType      string
		expectedResult     bool
	}{
		{
			description:        "should return true when condition exists with status True",
			existingConditions: []metav1.Condition{readyTrueCondition(), availableFalseCondition()},
			conditionType:      "Ready",
			expectedResult:     true,
		},
		{
			description:        "should return true when condition exists with status False",
			existingConditions: []metav1.Condition{readyFalseCondition(), availableTrueCondition()},
			conditionType:      "Ready",
			expectedResult:     true,
		},
		{
			description:        "should return true when condition exists with status Unknown",
			existingConditions: []metav1.Condition{readyUnknownCondition()},
			conditionType:      "Ready",
			expectedResult:     true,
		},
		{
			description:        "should return false when condition does not exist",
			existingConditions: []metav1.Condition{availableTrueCondition()},
			conditionType:      "Ready",
			expectedResult:     false,
		},
		{
			description:        "should return false when conditions list is empty",
			existingConditions: []metav1.Condition{},
			conditionType:      "Ready",
			expectedResult:     false,
		},
		{
			description:        "should return false when conditions list is nil",
			existingConditions: nil,
			conditionType:      "Ready",
			expectedResult:     false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			assert.Equal(t, tc.expectedResult, HasCondition(tc.existingConditions, tc.conditionType))
		})
	}
}

func TestGetConditionStatus(t *testing.T) {
	testCases := []struct {
		description        string
		existingConditions []metav1.Condition
		conditionType      string
		expectedStatus     *metav1.ConditionStatus
	}{
		{
			description:        "should return True status when condition exists and is True",
			existingConditions: []metav1.Condition{readyTrueCondition(), availableFalseCondition()},
			conditionType:      "Ready",
			expectedStatus:     ptr.To(metav1.ConditionTrue),
		},
		{
			description:        "should return False status when condition exists and is False",
			existingConditions: []metav1.Condition{readyFalseCondition(), availableTrueCondition()},
			conditionType:      "Ready",
			expectedStatus:     ptr.To(metav1.ConditionFalse),
		},
		{
			description:        "should return Unknown status when condition exists and is Unknown",
			existingConditions: []metav1.Condition{readyUnknownCondition(), availableTrueCondition()},
			conditionType:      "Ready",
			expectedStatus:     ptr.To(metav1.ConditionUnknown),
		},
		{
			description:        "should return nil when condition does not exist",
			existingConditions: []metav1.Condition{availableTrueCondition()},
			conditionType:      "Ready",
			expectedStatus:     nil,
		},
		{
			description:        "should return nil when conditions list is empty",
			existingConditions: []metav1.Condition{},
			conditionType:      "Ready",
			expectedStatus:     nil,
		},
		{
			description:        "should return nil when conditions list is nil",
			existingConditions: nil,
			conditionType:      "Ready",
			expectedStatus:     nil,
		},
		{
			description: "should return correct status when multiple conditions with same type exist (first match)",
			existingConditions: []metav1.Condition{
				readyTrueCondition(),
				readyFalseCondition(), // This one should be ignored
				availableUnknownCondition(),
			},
			conditionType:  "Ready",
			expectedStatus: ptr.To(metav1.ConditionTrue),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			result := GetConditionStatus(tc.existingConditions, tc.conditionType)

			if tc.expectedStatus == nil {
				assert.Nil(t, result)
			} else {
				assert.NotNil(t, result)
				assert.Equal(t, *tc.expectedStatus, *result)
			}
		})
	}
}

// Test helper functions
func newSimpleCondition(condType string, status metav1.ConditionStatus) metav1.Condition {
	return metav1.Condition{
		Type:   condType,
		Status: status,
	}
}

// Common condition builders
func readyTrueCondition() metav1.Condition {
	return newSimpleCondition("Ready", metav1.ConditionTrue)
}

func readyFalseCondition() metav1.Condition {
	return newSimpleCondition("Ready", metav1.ConditionFalse)
}

func readyUnknownCondition() metav1.Condition {
	return newSimpleCondition("Ready", metav1.ConditionUnknown)
}

func availableTrueCondition() metav1.Condition {
	return newSimpleCondition("Available", metav1.ConditionTrue)
}

func availableFalseCondition() metav1.Condition {
	return newSimpleCondition("Available", metav1.ConditionFalse)
}

func availableUnknownCondition() metav1.Condition {
	return newSimpleCondition("Available", metav1.ConditionUnknown)
}
