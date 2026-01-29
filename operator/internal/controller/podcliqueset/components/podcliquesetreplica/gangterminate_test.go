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

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestGetMinAvailableBreachedPCSGInfoWithEffectiveDelay(t *testing.T) {
	// Use a fixed time for consistent test results
	now := time.Now()
	pcsTerminationDelay := 5 * time.Minute
	pcsgOverrideDelay := 2 * time.Minute

	tests := []struct {
		name                  string
		pcsgs                 []grovecorev1alpha1.PodCliqueScalingGroup
		pcs                   *grovecorev1alpha1.PodCliqueSet
		since                 time.Time
		expectedBreachedNames []string
		expectedMinWaitFor    time.Duration
	}{
		// When there are no PCSGs to check, there are no MinAvailableBreached conditions to monitor, so there's nothing to wait for.
		{
			name:  "no_pcsgs",
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{},
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       types.UID("test-uid"),
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						TerminationDelay: &metav1.Duration{Duration: pcsTerminationDelay},
					},
				},
			},
			since:                 now,
			expectedBreachedNames: []string{},
			expectedMinWaitFor:    0,
		},
		// PCSG exists but has no MinAvailableBreached condition, so nothing is breached and nothing to wait for.
		{
			name: "pcsg_without_condition",
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-sg1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPodCliqueScalingGroup: "sg1",
						},
					},
					Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
						Conditions: []metav1.Condition{},
					},
				},
			},
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       types.UID("test-uid"),
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						TerminationDelay: &metav1.Duration{Duration: pcsTerminationDelay},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{Name: "sg1", CliqueNames: []string{"clique1"}},
						},
					},
				},
			},
			since:                 now,
			expectedBreachedNames: []string{},
			expectedMinWaitFor:    0,
		},
		// PCSG has MinAvailableBreached condition but it's false, so nothing is breached and nothing to wait for.
		{
			name: "pcsg_with_condition_false",
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-sg1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPodCliqueScalingGroup: "sg1",
						},
					},
					Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:   apiconstants.ConditionTypeMinAvailableBreached,
								Status: metav1.ConditionFalse,
							},
						},
					},
				},
			},
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       types.UID("test-uid"),
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						TerminationDelay: &metav1.Duration{Duration: pcsTerminationDelay},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{Name: "sg1", CliqueNames: []string{"clique1"}},
						},
					},
				},
			},
			since:                 now,
			expectedBreachedNames: []string{},
			expectedMinWaitFor:    0,
		},
		// PCSG is breached but missing the required label to identify the scaling group config, so it's skipped.
		{
			name: "pcsg_breached_missing_label",
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-sg1",
						Namespace: "default",
						Labels:    map[string]string{}, // Missing LabelPodCliqueScalingGroup
					},
					Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:               apiconstants.ConditionTypeMinAvailableBreached,
								Status:             metav1.ConditionTrue,
								LastTransitionTime: metav1.NewTime(now.Add(-10 * time.Minute)),
							},
						},
					},
				},
			},
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       types.UID("test-uid"),
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						TerminationDelay: &metav1.Duration{Duration: pcsTerminationDelay},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{Name: "sg1", CliqueNames: []string{"clique1"}},
						},
					},
				},
			},
			since:                 now,
			expectedBreachedNames: []string{},
			expectedMinWaitFor:    0,
		},
		// PCSG is breached and terminationDelay (5 mins) has expired (breached 10 mins ago), returns negative wait time.
		{
			name: "pcsg_breached_delay_expired",
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-sg1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPodCliqueScalingGroup: "sg1",
						},
					},
					Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:               apiconstants.ConditionTypeMinAvailableBreached,
								Status:             metav1.ConditionTrue,
								LastTransitionTime: metav1.NewTime(now.Add(-10 * time.Minute)), // 10 mins ago, delay is 5 mins
							},
						},
					},
				},
			},
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       types.UID("test-uid"),
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						TerminationDelay: &metav1.Duration{Duration: pcsTerminationDelay},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{Name: "sg1", CliqueNames: []string{"clique1"}},
						},
					},
				},
			},
			since:                 now,
			expectedBreachedNames: []string{"test-pcs-0-sg1"},
			expectedMinWaitFor:    -5 * time.Minute, // 5 mins - 10 mins = -5 mins (expired)
		},
		// PCSG is breached but terminationDelay (5 mins) hasn't expired yet (breached 2 mins ago), returns 3 mins remaining.
		{
			name: "pcsg_breached_delay_not_expired",
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-sg1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPodCliqueScalingGroup: "sg1",
						},
					},
					Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:               apiconstants.ConditionTypeMinAvailableBreached,
								Status:             metav1.ConditionTrue,
								LastTransitionTime: metav1.NewTime(now.Add(-2 * time.Minute)), // 2 mins ago, delay is 5 mins
							},
						},
					},
				},
			},
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       types.UID("test-uid"),
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						TerminationDelay: &metav1.Duration{Duration: pcsTerminationDelay},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{Name: "sg1", CliqueNames: []string{"clique1"}},
						},
					},
				},
			},
			since:                 now,
			expectedBreachedNames: []string{"test-pcs-0-sg1"},
			expectedMinWaitFor:    3 * time.Minute, // 5 mins - 2 mins = 3 mins remaining
		},
		// PCSG has override terminationDelay (2 mins) which has expired (breached 3 mins ago), returns negative wait time.
		{
			name: "pcsg_with_override_delay_expired",
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-sg1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPodCliqueScalingGroup: "sg1",
						},
					},
					Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:               apiconstants.ConditionTypeMinAvailableBreached,
								Status:             metav1.ConditionTrue,
								LastTransitionTime: metav1.NewTime(now.Add(-3 * time.Minute)), // 3 mins ago, override delay is 2 mins
							},
						},
					},
				},
			},
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       types.UID("test-uid"),
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						TerminationDelay: &metav1.Duration{Duration: pcsTerminationDelay},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:             "sg1",
								CliqueNames:      []string{"clique1"},
								TerminationDelay: &metav1.Duration{Duration: pcsgOverrideDelay}, // 2 min override
							},
						},
					},
				},
			},
			since:                 now,
			expectedBreachedNames: []string{"test-pcs-0-sg1"},
			expectedMinWaitFor:    -1 * time.Minute, // 2 mins - 3 mins = -1 min (expired)
		},
		// PCSG has override terminationDelay (2 mins) which hasn't expired (breached 1 min ago), returns 1 min remaining.
		{
			name: "pcsg_with_override_delay_not_expired",
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-sg1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPodCliqueScalingGroup: "sg1",
						},
					},
					Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:               apiconstants.ConditionTypeMinAvailableBreached,
								Status:             metav1.ConditionTrue,
								LastTransitionTime: metav1.NewTime(now.Add(-1 * time.Minute)), // 1 min ago, override delay is 2 mins
							},
						},
					},
				},
			},
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       types.UID("test-uid"),
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						TerminationDelay: &metav1.Duration{Duration: pcsTerminationDelay},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:             "sg1",
								CliqueNames:      []string{"clique1"},
								TerminationDelay: &metav1.Duration{Duration: pcsgOverrideDelay}, // 2 min override
							},
						},
					},
				},
			},
			since:                 now,
			expectedBreachedNames: []string{"test-pcs-0-sg1"},
			expectedMinWaitFor:    1 * time.Minute, // 2 mins - 1 min = 1 min remaining
		},
		// Multiple PCSGs with different states: one expired, one not breached, one not expired; returns minimum wait time.
		{
			name: "multiple_pcsgs_mixed_states",
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-sg1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPodCliqueScalingGroup: "sg1",
						},
					},
					Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:               apiconstants.ConditionTypeMinAvailableBreached,
								Status:             metav1.ConditionTrue,
								LastTransitionTime: metav1.NewTime(now.Add(-10 * time.Minute)), // expired
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-sg2",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPodCliqueScalingGroup: "sg2",
						},
					},
					Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:               apiconstants.ConditionTypeMinAvailableBreached,
								Status:             metav1.ConditionFalse, // not breached
								LastTransitionTime: metav1.NewTime(now.Add(-1 * time.Minute)),
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-sg3",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPodCliqueScalingGroup: "sg3",
						},
					},
					Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:               apiconstants.ConditionTypeMinAvailableBreached,
								Status:             metav1.ConditionTrue,
								LastTransitionTime: metav1.NewTime(now.Add(-2 * time.Minute)), // not expired, 3 mins remaining
							},
						},
					},
				},
			},
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       types.UID("test-uid"),
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						TerminationDelay: &metav1.Duration{Duration: pcsTerminationDelay},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{Name: "sg1", CliqueNames: []string{"clique1"}},
							{Name: "sg2", CliqueNames: []string{"clique2"}},
							{Name: "sg3", CliqueNames: []string{"clique3"}},
						},
					},
				},
			},
			since:                 now,
			expectedBreachedNames: []string{"test-pcs-0-sg1", "test-pcs-0-sg3"},
			expectedMinWaitFor:    -5 * time.Minute, // min of -5 and 3 = -5 (sg1 expired first)
		},
		// PCSG is breached but config not found in PCS, falls back to PCS default terminationDelay.
		{
			name: "pcsg_breached_config_not_found",
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-sg1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPodCliqueScalingGroup: "sg1",
						},
					},
					Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:               apiconstants.ConditionTypeMinAvailableBreached,
								Status:             metav1.ConditionTrue,
								LastTransitionTime: metav1.NewTime(now.Add(-10 * time.Minute)),
							},
						},
					},
				},
			},
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       types.UID("test-uid"),
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						TerminationDelay: &metav1.Duration{Duration: pcsTerminationDelay},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							// sg1 config is missing - uses PCS default
							{Name: "sg2", CliqueNames: []string{"clique2"}},
						},
					},
				},
			},
			since:                 now,
			expectedBreachedNames: []string{"test-pcs-0-sg1"},
			expectedMinWaitFor:    -5 * time.Minute, // Uses PCS default: 5 mins - 10 mins = -5 mins
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			breachedNames, minWaitFor := getMinAvailableBreachedPCSGInfoWithEffectiveDelay(tt.pcsgs, tt.pcs, tt.since)

			assert.ElementsMatch(t, tt.expectedBreachedNames, breachedNames, "breached PCSG names mismatch")
			assert.Equal(t, tt.expectedMinWaitFor, minWaitFor, "minWaitFor mismatch")
		})
	}
}

func TestIsPCLQInPCSG(t *testing.T) {
	tests := []struct {
		name        string
		pclqName    string
		pcsgConfigs []grovecorev1alpha1.PodCliqueScalingGroupConfig
		expected    bool
	}{
		// No PCSG configs means PCLQ is not in any PCSG.
		{
			name:        "empty_configs",
			pclqName:    "clique1",
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{},
			expected:    false,
		},
		// PCLQ is found in first PCSG config.
		{
			name:     "pclq_in_pcsg",
			pclqName: "clique1",
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg1", CliqueNames: []string{"clique1", "clique2"}},
			},
			expected: true,
		},
		// PCLQ is not found in any PCSG config.
		{
			name:     "pclq_not_in_pcsg",
			pclqName: "clique3",
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg1", CliqueNames: []string{"clique1", "clique2"}},
			},
			expected: false,
		},
		// PCLQ is found in second PCSG config.
		{
			name:     "pclq_in_second_pcsg",
			pclqName: "clique3",
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg1", CliqueNames: []string{"clique1", "clique2"}},
				{Name: "sg2", CliqueNames: []string{"clique3", "clique4"}},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isPCLQInPCSG(tt.pclqName, tt.pcsgConfigs)
			assert.Equal(t, tt.expected, result)
		})
	}
}
