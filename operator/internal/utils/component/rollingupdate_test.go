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

package component

import (
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"
)

func TestEffectiveMaxUnavailable(t *testing.T) {
	testCases := []struct {
		description   string
		rollingUpdate *grovecorev1alpha1.RollingUpdateConfiguration
		want          int
	}{
		{
			description:   "nil rollingUpdate returns the RollingRecreate default",
			rollingUpdate: nil,
			want:          int(DefaultRollingRecreateMaxUnavailable),
		},
		{
			description:   "nil MaxUnavailable returns the RollingRecreate default",
			rollingUpdate: &grovecorev1alpha1.RollingUpdateConfiguration{},
			want:          int(DefaultRollingRecreateMaxUnavailable),
		},
		{
			description:   "configured MaxUnavailable is returned",
			rollingUpdate: &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](3)},
			want:          3,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			assert.Equal(t, tc.want, EffectiveMaxUnavailable(tc.rollingUpdate))
		})
	}
}

func TestComputeAllowedBudget(t *testing.T) {
	tests := []struct {
		description             string
		desiredNumUnits         int
		numReadyUnits           int
		effectiveMaxUnavailable int
		want                    int
	}{
		{"default budget of 1 with all available", 3, 3, 1, 1},
		{"budget of 1 when all replicas are required and ready", 2, 2, 1, 1},
		{"budget exhausted by an in-flight disruption", 3, 2, 1, 0},
		{"maxUnavailable allows multiple disruptions", 5, 5, 2, 2},
		{"full disruption when maxUnavailable equals replicas", 4, 4, 4, 4},
		{"floored at zero when unavailable exceeds the budget", 3, 1, 1, 0},
		{"budget accounts for existing unavailable units", 6, 5, 3, 2},
	}
	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			assert.Equal(t, tt.want, ComputeAllowedBudget(tt.desiredNumUnits, tt.numReadyUnits, tt.effectiveMaxUnavailable))
		})
	}
}
