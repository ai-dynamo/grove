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

package podcliquesetreplica

import (
	"testing"
	"time"

	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestGetMinAvailableBreachedPCSGInfoGangTerminationGate pins the PCS-level gate added to break
// the post-recycle loop: a PCSG that is currently MinAvailableBreached=True but already carries
// GangTerminationInProgress=True must NOT appear in the breach-candidate list — a previous
// recycle is in flight, the action would just churn.
func TestGetMinAvailableBreachedPCSGInfoGangTerminationGate(t *testing.T) {
	pastTransition := metav1.NewTime(time.Now().Add(-1 * time.Hour))
	now := time.Now()
	terminationDelay := 10 * time.Second

	breachedTrue := metav1.Condition{
		Type:               apiconstants.ConditionTypeMinAvailableBreached,
		Status:             metav1.ConditionTrue,
		Reason:             apiconstants.ConditionReasonScheduledReplicasBelowMinAvailable,
		LastTransitionTime: pastTransition,
	}
	inProgressTrue := metav1.Condition{
		Type:               apiconstants.ConditionTypeGangTerminationInProgress,
		Status:             metav1.ConditionTrue,
		Reason:             apiconstants.ConditionReasonGangTerminationActive,
		LastTransitionTime: pastTransition,
	}

	tests := []struct {
		name       string
		pcsg       grovecorev1alpha1.PodCliqueScalingGroup
		wantInList bool
	}{
		{
			name: "breached, no in-progress flag — candidate for fire",
			pcsg: grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "pcsg-fresh-breach"},
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					Conditions: []metav1.Condition{breachedTrue},
				},
			},
			wantInList: true,
		},
		{
			name: "breached AND in-progress flag set — skipped (recycle in flight)",
			pcsg: grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "pcsg-recycling"},
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					Conditions: []metav1.Condition{breachedTrue, inProgressTrue},
				},
			},
			wantInList: false,
		},
		{
			name: "not breached, in-progress flag still set — skipped (no action needed)",
			pcsg: grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "pcsg-recovered-but-stale-flag"},
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					Conditions: []metav1.Condition{
						{Type: apiconstants.ConditionTypeMinAvailableBreached, Status: metav1.ConditionFalse, LastTransitionTime: pastTransition},
						inProgressTrue,
					},
				},
			},
			wantInList: false,
		},
		{
			name: "no MinAvailableBreached condition at all — skipped",
			pcsg: grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "pcsg-fresh"},
			},
			wantInList: false,
		},
		{
			name: "breached but never healthy (initial startup) — skipped by wasHealthy gate",
			pcsg: grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "pcsg-initial-startup",
					CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Second)),
				},
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					Conditions: []metav1.Condition{
						{
							Type:               apiconstants.ConditionTypeMinAvailableBreached,
							Status:             metav1.ConditionTrue,
							Reason:             apiconstants.ConditionReasonScheduledReplicasBelowMinAvailable,
							LastTransitionTime: metav1.NewTime(now.Add(-1 * time.Second)),
						},
					},
				},
			},
			wantInList: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			names, _ := getMinAvailableBreachedPCSGInfo([]grovecorev1alpha1.PodCliqueScalingGroup{tc.pcsg}, terminationDelay, now)
			if tc.wantInList {
				assert.Equal(t, []string{tc.pcsg.Name}, names, "expected PCSG in breach candidate list")
			} else {
				assert.Empty(t, names, "expected PCSG to be filtered out")
			}
		})
	}
}
