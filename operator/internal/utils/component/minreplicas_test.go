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

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func initSpec(minReplicas *int32) *grovecorev1beta1.InitSpec {
	return &grovecorev1beta1.InitSpec{MinReplicas: minReplicas}
}

func availabilityTermination(minReplicas *int32, breachTolerance *time.Duration) *grovecorev1beta1.TerminationSpec {
	availability := &grovecorev1beta1.AvailabilityPolicy{MinReplicas: minReplicas}
	if breachTolerance != nil {
		availability.BreachTolerance = &metav1.Duration{Duration: *breachTolerance}
	}
	return &grovecorev1beta1.TerminationSpec{Resource: &grovecorev1beta1.ResourceTerminationSpec{Availability: availability}}
}

// TestInitAndAvailabilityAccessors pins how init.minReplicas and availability.minReplicas resolve, including
// the defaults applied to objects that were not re-admitted.
func TestInitAndAvailabilityAccessors(t *testing.T) {
	testCases := []struct {
		description      string
		init             *grovecorev1beta1.InitSpec
		termination      *grovecorev1beta1.TerminationSpec
		wantInit         int32
		wantAvailability int32
		wantHasAvail     bool
		wantReady        int32
		wantScaleFloor   int32
	}{
		{
			description:      "both set",
			init:             initSpec(ptr.To[int32](3)),
			termination:      availabilityTermination(ptr.To[int32](2), nil),
			wantInit:         3,
			wantAvailability: 2,
			wantHasAvail:     true,
			wantReady:        2,
			wantScaleFloor:   2,
		},
		{
			description:      "init unset defaults to availability",
			termination:      availabilityTermination(ptr.To[int32](2), nil),
			wantInit:         2,
			wantAvailability: 2,
			wantHasAvail:     true,
			wantReady:        2,
			wantScaleFloor:   2,
		},
		{
			description:      "availability with unset minReplicas defaults to 1",
			init:             initSpec(ptr.To[int32](3)),
			termination:      availabilityTermination(nil, nil),
			wantInit:         3,
			wantAvailability: 1,
			wantHasAvail:     true,
			wantReady:        1,
			wantScaleFloor:   1,
		},
		{
			description:    "no availability: never in breach, ready threshold and floor fall back",
			init:           initSpec(ptr.To[int32](3)),
			wantInit:       3,
			wantReady:      3,
			wantScaleFloor: 1,
		},
		{
			description:    "nothing set defaults to 1",
			wantInit:       1,
			wantReady:      1,
			wantScaleFloor: 1,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			pclqSpec := grovecorev1beta1.PodCliqueSpec{Replicas: 5, Init: tc.init, Termination: tc.termination}
			pcsgSpec := grovecorev1beta1.PodCliqueScalingGroupSpec{Replicas: 5, Init: tc.init, Termination: tc.termination}
			pcsgConfig := grovecorev1beta1.PodCliqueScalingGroupConfig{Init: tc.init, Termination: tc.termination}

			assert.Equal(t, tc.wantInit, PCLQInitMinReplicas(&pclqSpec))
			assert.Equal(t, tc.wantInit, PCSGInitMinReplicas(&pcsgSpec))
			assert.Equal(t, tc.wantInit, PCSGConfigInitMinReplicas(&pcsgConfig))

			pclqAvailability, ok := PCLQAvailabilityMinReplicas(&pclqSpec)
			assert.Equal(t, tc.wantHasAvail, ok)
			assert.Equal(t, tc.wantAvailability, pclqAvailability)
			pcsgAvailability, ok := PCSGAvailabilityMinReplicas(&pcsgSpec)
			assert.Equal(t, tc.wantHasAvail, ok)
			assert.Equal(t, tc.wantAvailability, pcsgAvailability)

			assert.Equal(t, tc.wantReady, PCLQReadyThreshold(&pclqSpec))
			assert.Equal(t, tc.wantReady, PCSGReadyThreshold(&pcsgSpec))
			assert.Equal(t, tc.wantScaleFloor, ScaleFloor(tc.termination))
		})
	}
}

// TestUpdateMinReplicasAccessors pins that the minimum update unit is rollingUpdate.minReplicas under Coherent
// (defaulting to availability.minReplicas, or 1) and init.minReplicas under the other strategies, and that a
// PCSG member always uses its own init.minReplicas.
func TestUpdateMinReplicasAccessors(t *testing.T) {
	newPCS := func(strategy grovecorev1beta1.UpdateStrategyType, rollingUpdate *grovecorev1beta1.RollingUpdateConfiguration) *grovecorev1beta1.PodCliqueSet {
		return &grovecorev1beta1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: "pcs"},
			Spec: grovecorev1beta1.PodCliqueSetSpec{
				UpdateStrategy: &grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: strategy},
				Template: grovecorev1beta1.PodCliqueSetTemplateSpec{
					Cliques: []*grovecorev1beta1.PodCliqueTemplateSpec{
						{
							Name:          "fe",
							RollingUpdate: rollingUpdate,
							Spec:          grovecorev1beta1.PodCliqueSpec{Replicas: 5, Init: initSpec(ptr.To[int32](4)), Termination: availabilityTermination(ptr.To[int32](2), nil)},
						},
						{
							Name: "pf-leader",
							Spec: grovecorev1beta1.PodCliqueSpec{Replicas: 3, Init: initSpec(ptr.To[int32](3))},
						},
					},
					PodCliqueScalingGroupConfigs: []grovecorev1beta1.PodCliqueScalingGroupConfig{
						{
							Name:          "pf",
							CliqueNames:   []string{"pf-leader"},
							RollingUpdate: rollingUpdate,
							Init:          initSpec(ptr.To[int32](4)),
							Termination:   availabilityTermination(ptr.To[int32](2), nil),
						},
					},
				},
			},
		}
	}
	feTemplate := func(pcs *grovecorev1beta1.PodCliqueSet) *grovecorev1beta1.PodCliqueTemplateSpec {
		return pcs.Spec.Template.Cliques[0]
	}
	pfConfig := func(pcs *grovecorev1beta1.PodCliqueSet) *grovecorev1beta1.PodCliqueScalingGroupConfig {
		return &pcs.Spec.Template.PodCliqueScalingGroupConfigs[0]
	}
	fePCLQ := func(pcs *grovecorev1beta1.PodCliqueSet) *grovecorev1beta1.PodClique {
		return &grovecorev1beta1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: "pcs-0-fe", Labels: map[string]string{apicommon.LabelPartOfKey: "pcs", apicommon.LabelPodCliqueSetReplicaIndex: "0"}}, Spec: feTemplate(pcs).Spec}
	}
	pfPCSG := func(pcs *grovecorev1beta1.PodCliqueSet) *grovecorev1beta1.PodCliqueScalingGroup {
		return &grovecorev1beta1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "pcs-0-pf", Labels: map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "0"}},
			Spec:       grovecorev1beta1.PodCliqueScalingGroupSpec{Replicas: 4, Init: pfConfig(pcs).Init, Termination: pfConfig(pcs).Termination},
		}
	}
	memberPCLQ := &grovecorev1beta1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: "pcs-0-pf-0-pf-leader", Labels: map[string]string{apicommon.LabelPodCliqueScalingGroup: "pcs-0-pf"}},
		Spec:       grovecorev1beta1.PodCliqueSpec{Replicas: 3, Init: initSpec(ptr.To[int32](3))},
	}

	testCases := []struct {
		description   string
		strategy      grovecorev1beta1.UpdateStrategyType
		rollingUpdate *grovecorev1beta1.RollingUpdateConfiguration
		want          int32
	}{
		{description: "Coherent with rollingUpdate.minReplicas", strategy: grovecorev1beta1.CoherentStrategy, rollingUpdate: &grovecorev1beta1.RollingUpdateConfiguration{MinReplicas: ptr.To[int32](3)}, want: 3},
		{description: "Coherent defaults to availability.minReplicas", strategy: grovecorev1beta1.CoherentStrategy, want: 2},
		{description: "RollingRecreate uses init.minReplicas", strategy: grovecorev1beta1.RollingRecreateStrategy, want: 4},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			pcs := newPCS(tc.strategy, tc.rollingUpdate)
			assert.Equal(t, tc.want, CliqueTemplateUpdateMinReplicas(pcs, feTemplate(pcs)))
			assert.Equal(t, tc.want, PCSGConfigUpdateMinReplicas(pcs, pfConfig(pcs)))
			assert.Equal(t, tc.want, PCLQUpdateMinReplicas(pcs, fePCLQ(pcs)))
			assert.Equal(t, tc.want, PCSGUpdateMinReplicas(pcs, pfPCSG(pcs)))
			assert.Equal(t, int32(3), PCLQUpdateMinReplicas(pcs, memberPCLQ))
		})
	}
}

// TestBreachToleranceAccessors pins that the breach tolerance is the child's own value, then its template
// component's value, and 4h when neither is set.
func TestBreachToleranceAccessors(t *testing.T) {
	thirty := 30 * time.Second
	ten := 10 * time.Second
	testCases := []struct {
		description   string
		ownTolerance  *time.Duration
		tmplTolerance *time.Duration
		want          time.Duration
	}{
		{description: "own value wins", ownTolerance: &ten, tmplTolerance: &thirty, want: ten},
		{description: "falls back to the template", tmplTolerance: &thirty, want: thirty},
		{description: "defaults to 4h", want: DefaultBreachTolerance},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			pcs := &grovecorev1beta1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: "pcs"},
				Spec: grovecorev1beta1.PodCliqueSetSpec{
					Template: grovecorev1beta1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1beta1.PodCliqueTemplateSpec{
							{Name: "fe", Spec: grovecorev1beta1.PodCliqueSpec{Termination: availabilityTermination(nil, tc.tmplTolerance)}},
						},
						PodCliqueScalingGroupConfigs: []grovecorev1beta1.PodCliqueScalingGroupConfig{
							{Name: "pf", Termination: availabilityTermination(nil, tc.tmplTolerance)},
						},
					},
				},
			}
			pclq := &grovecorev1beta1.PodClique{
				ObjectMeta: metav1.ObjectMeta{Name: "pcs-0-fe", Labels: map[string]string{apicommon.LabelPartOfKey: "pcs", apicommon.LabelPodCliqueSetReplicaIndex: "0"}},
				Spec:       grovecorev1beta1.PodCliqueSpec{Termination: availabilityTermination(nil, tc.ownTolerance)},
			}
			pcsg := &grovecorev1beta1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "pcs-0-pf", Labels: map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "0"}},
				Spec:       grovecorev1beta1.PodCliqueScalingGroupSpec{Termination: availabilityTermination(nil, tc.ownTolerance)},
			}
			assert.Equal(t, tc.want, PCLQBreachTolerance(pcs, pclq))
			assert.Equal(t, tc.want, PCSGBreachTolerance(pcs, pcsg))
			assert.Equal(t, ptr.Deref(tc.ownTolerance, DefaultBreachTolerance), PCLQBreachTolerance(nil, pclq))
		})
	}
}
