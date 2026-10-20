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
	"time"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// TestMinReplicaAccessors pins that every min-replica accessor reads MinAvailable, and that a nil
// MinAvailable reads as the API default of 1.
func TestMinReplicaAccessors(t *testing.T) {
	testCases := []struct {
		description  string
		minAvailable *int32
		want         int32
	}{
		{description: "set MinAvailable is returned", minAvailable: ptr.To[int32](3), want: 3},
		{description: "nil MinAvailable defaults to 1", minAvailable: nil, want: 1},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			pcs := &grovecorev1beta1.PodCliqueSet{}
			pclqSpec := grovecorev1beta1.PodCliqueSpec{Replicas: 5, MinAvailable: tc.minAvailable}
			pcsgSpec := grovecorev1beta1.PodCliqueScalingGroupSpec{Replicas: 5, MinAvailable: tc.minAvailable}
			pcsgConfig := grovecorev1beta1.PodCliqueScalingGroupConfig{MinAvailable: tc.minAvailable}

			assert.Equal(t, tc.want, PCLQInitMinReplicas(&pclqSpec))
			assert.Equal(t, tc.want, PCSGInitMinReplicas(&pcsgSpec))
			assert.Equal(t, tc.want, PCSGConfigInitMinReplicas(&pcsgConfig))

			pclqAvailability, ok := PCLQAvailabilityMinReplicas(&pclqSpec)
			assert.True(t, ok)
			assert.Equal(t, tc.want, pclqAvailability)
			pcsgAvailability, ok := PCSGAvailabilityMinReplicas(&pcsgSpec)
			assert.True(t, ok)
			assert.Equal(t, tc.want, pcsgAvailability)

			assert.Equal(t, tc.want, PCLQReadyThreshold(&pclqSpec))
			assert.Equal(t, tc.want, PCSGReadyThreshold(&pcsgSpec))

			assert.Equal(t, tc.want, CliqueTemplateUpdateMinReplicas(pcs, &grovecorev1beta1.PodCliqueTemplateSpec{Spec: pclqSpec}))
			assert.Equal(t, tc.want, PCSGConfigUpdateMinReplicas(pcs, &pcsgConfig))
			assert.Equal(t, tc.want, PCLQUpdateMinReplicas(pcs, &grovecorev1beta1.PodClique{Spec: pclqSpec}))
			assert.Equal(t, tc.want, PCSGUpdateMinReplicas(pcs, &grovecorev1beta1.PodCliqueScalingGroup{Spec: pcsgSpec}))
		})
	}
}

// TestBreachToleranceAccessors pins that the breach tolerance is the PodCliqueSet TerminationDelay for both
// PodCliques and PodCliqueScalingGroups, and defaults to 4h when unset.
func TestBreachToleranceAccessors(t *testing.T) {
	testCases := []struct {
		description      string
		terminationDelay *metav1.Duration
		want             time.Duration
	}{
		{description: "set TerminationDelay is returned", terminationDelay: &metav1.Duration{Duration: 30 * time.Second}, want: 30 * time.Second},
		{description: "nil TerminationDelay defaults to 4h", terminationDelay: nil, want: 4 * time.Hour},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			pcs := &grovecorev1beta1.PodCliqueSet{
				Spec: grovecorev1beta1.PodCliqueSetSpec{
					Template: grovecorev1beta1.PodCliqueSetTemplateSpec{TerminationDelay: tc.terminationDelay},
				},
			}
			assert.Equal(t, tc.want, PCLQBreachTolerance(pcs, &grovecorev1beta1.PodClique{}))
			assert.Equal(t, tc.want, PCSGBreachTolerance(pcs, &grovecorev1beta1.PodCliqueScalingGroup{}))
		})
	}
}
