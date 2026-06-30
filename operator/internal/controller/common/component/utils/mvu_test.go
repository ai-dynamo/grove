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

package utils

import (
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
)

func TestComputeMVUTemplateFromPCSTemplateSpec(t *testing.T) {
	tests := []struct {
		name                    string
		pcs                     *grovecorev1alpha1.PodCliqueSet
		expectedStandalonePCLQs map[string]int32
		expectedPCSGs           map[string]int32
	}{
		{
			name: "standalone PCLQs only",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pcs"},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To(int32(2))}},
							{Name: "frontend", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To(int32(1))}},
						},
					},
				},
			},
			expectedStandalonePCLQs: map[string]int32{"worker": 2, "frontend": 1},
			expectedPCSGs:           map[string]int32{},
		},
		{
			name: "PCSGs only",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pcs"},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "sg-worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2, MinAvailable: ptr.To(int32(2))}},
						},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{Name: "scaling-group", Replicas: ptr.To(int32(4)), MinAvailable: ptr.To(int32(2)), CliqueNames: []string{"sg-worker"}},
						},
					},
				},
			},
			expectedStandalonePCLQs: map[string]int32{},
			expectedPCSGs:           map[string]int32{"scaling-group": 2},
		},
		{
			name: "mixed standalone PCLQs and PCSGs",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pcs"},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "frontend", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To(int32(2))}},
							{Name: "prefill-worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To(int32(2))}},
							{Name: "decode-worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4, MinAvailable: ptr.To(int32(2))}},
						},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{Name: "prefill", Replicas: ptr.To(int32(4)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"prefill-worker"}},
							{Name: "decode", Replicas: ptr.To(int32(3)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"decode-worker"}},
						},
					},
				},
			},
			expectedStandalonePCLQs: map[string]int32{"frontend": 2},
			expectedPCSGs:           map[string]int32{"prefill": 1, "decode": 1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			template := ComputeMVUTemplateFromPCSTemplateSpec(tc.pcs)
			assert.Equal(t, tc.expectedStandalonePCLQs, template.StandalonePCLQs)
			assert.Equal(t, tc.expectedPCSGs, template.PCSGs)
		})
	}
}

func TestGetStandalonePCLQReplicasFromSpec(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pcs"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "frontend", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To(int32(2))}},
					{Name: "prefill-worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To(int32(2))}},
				},
				PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
					{Name: "prefill", Replicas: ptr.To(int32(4)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"prefill-worker"}},
				},
			},
		},
	}

	result := GetStandalonePCLQReplicasFromPCSTemplateSpec(pcs)
	assert.Equal(t, map[string]int32{"frontend": 5}, result)
}

func TestGetPCSGReplicasFromSpec(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pcs"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
					{Name: "prefill", Replicas: ptr.To(int32(4)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"prefill-worker"}},
					{Name: "decode", Replicas: ptr.To(int32(3)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"decode-worker"}},
				},
			},
		},
	}

	result := GetPCSGReplicasFromPCSTemplateSpec(pcs)
	assert.Equal(t, map[string]int32{"prefill": 4, "decode": 3}, result)
}

func TestNewPodGangEntryBuilder(t *testing.T) {
	t.Run("name encodes pcs name, replica index, and clock-based suffix", func(t *testing.T) {
		baseTime := time.Unix(0, 1748266985000000000)
		fakeClk := clocktesting.NewFakeClock(baseTime)
		builder := NewPodGangEntryBuilder("my-pcs", 0, "abc123", fakeClk)

		entry := builder(map[string]int32{"worker": 2}, map[string][]int32{"sg": {0}}, nil)

		assert.Equal(t, "my-pcs-0-1748266985000000000", entry.Name)
		assert.Equal(t, "abc123", entry.PodCliqueSetGenerationHash)
		assert.Equal(t, map[string]int32{"worker": 2}, entry.PodCliques)
		assert.Equal(t, map[string][]int32{"sg": {0}}, entry.PCSGReplicaIndices)
		assert.Nil(t, entry.DependsOn)
	})

	t.Run("counter salts successive calls under same nano", func(t *testing.T) {
		// FakeClock does not advance unless Step is called, so successive entries
		// share the same Now() but the +i salt makes their names distinct.
		fakeClk := clocktesting.NewFakeClock(time.Unix(0, 1000))
		builder := NewPodGangEntryBuilder("my-pcs", 0, "h", fakeClk)

		e1 := builder(nil, nil, nil)
		e2 := builder(nil, nil, nil)
		e3 := builder(nil, nil, nil)

		assert.Equal(t, "my-pcs-0-1000", e1.Name)
		assert.Equal(t, "my-pcs-0-1001", e2.Name)
		assert.Equal(t, "my-pcs-0-1002", e3.Name)
	})

	t.Run("clock advances after construction do not affect subsequent calls", func(t *testing.T) {
		// A builder represents one batch. The epoch is captured once at NewPodGangEntryBuilder
		// and shared across every entry the builder produces. Clock advances mid-batch are
		// ignored so all entries carry the same grove.io/epoch label, only the within-batch
		// counter +i changes between calls.
		fakeClk := clocktesting.NewFakeClock(time.Unix(0, 1000))
		builder := NewPodGangEntryBuilder("my-pcs", 0, "h", fakeClk)

		e1 := builder(nil, nil, nil)
		fakeClk.Step(500 * time.Nanosecond)
		e2 := builder(nil, nil, nil)

		assert.Equal(t, "my-pcs-0-1000", e1.Name)
		assert.Equal(t, "my-pcs-0-1001", e2.Name)
	})

	t.Run("two builder instances have independent counters", func(t *testing.T) {
		fakeClk := clocktesting.NewFakeClock(time.Unix(0, 5000))
		b1 := NewPodGangEntryBuilder("my-pcs", 0, "h", fakeClk)
		b2 := NewPodGangEntryBuilder("my-pcs", 1, "h", fakeClk)

		b1Entry := b1(nil, nil, nil) // i=0 → 5000
		b2Entry := b2(nil, nil, nil) // i=0 → 5000 (independent counter)

		assert.Equal(t, "my-pcs-0-5000", b1Entry.Name)
		assert.Equal(t, "my-pcs-1-5000", b2Entry.Name)
		// Different replica index segment guarantees uniqueness across builders even with same suffix.
	})

	t.Run("dependsOn is preserved", func(t *testing.T) {
		fakeClk := clocktesting.NewFakeClock(time.Unix(0, 1000))
		builder := NewPodGangEntryBuilder("my-pcs", 0, "h", fakeClk)

		e1 := builder(nil, nil, nil)
		e2 := builder(nil, nil, []string{e1.Name})

		assert.Equal(t, []string{e1.Name}, e2.DependsOn)
	})

	t.Run("all entries from one builder share the same grove.io/epoch label", func(t *testing.T) {
		fakeClk := clocktesting.NewFakeClock(time.Unix(0, 1000))
		builder := NewPodGangEntryBuilder("my-pcs", 0, "h", fakeClk)

		e1 := builder(nil, nil, nil)
		fakeClk.Step(500 * time.Nanosecond)
		e2 := builder(nil, nil, nil)
		e3 := builder(nil, nil, nil)

		assert.Equal(t, "1000", e1.Labels[apicommon.LabelEpoch])
		assert.Equal(t, "1000", e2.Labels[apicommon.LabelEpoch])
		assert.Equal(t, "1000", e3.Labels[apicommon.LabelEpoch])
	})

	t.Run("separate builders produce separate epoch labels", func(t *testing.T) {
		fakeClk := clocktesting.NewFakeClock(time.Unix(0, 1000))
		b1 := NewPodGangEntryBuilder("my-pcs", 0, "h", fakeClk)
		fakeClk.Step(500 * time.Nanosecond)
		b2 := NewPodGangEntryBuilder("my-pcs", 0, "h", fakeClk)

		e1 := b1(nil, nil, nil)
		e2 := b2(nil, nil, nil)

		assert.Equal(t, "1000", e1.Labels[apicommon.LabelEpoch])
		assert.Equal(t, "1500", e2.Labels[apicommon.LabelEpoch])
	})
}
