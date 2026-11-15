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

package expectations

import (
	"testing"
	"time"

	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestCurrentlyUpdatingClearIfSatisfiedWithoutExpectation(t *testing.T) {
	e := &CurrentlyUpdating{}

	assert.True(t, e.ClearIfSatisfied(currentlyUpdatingTestObjectMeta(), nil))
}

func TestCurrentlyUpdatingRecordAndClearIfSatisfied(t *testing.T) {
	objectMeta := currentlyUpdatingTestObjectMeta()
	recorded := []grovev1beta1.PodCliqueSetReplicaUpdateProgress{
		{ReplicaIndex: 0},
		{
			ReplicaIndex: 2,
			Replacement:  &grovev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
		},
	}

	t.Run("matching selection clears the expectation", func(t *testing.T) {
		e := &CurrentlyUpdating{}
		e.Record(objectMeta, recorded)

		assert.True(t, e.ClearIfSatisfied(objectMeta, recorded))
		// The expectation is removed, so a subsequent check with any value is satisfied.
		assert.True(t, e.ClearIfSatisfied(objectMeta, nil))
	})

	t.Run("selection match ignores entry order", func(t *testing.T) {
		e := &CurrentlyUpdating{}
		e.Record(objectMeta, recorded)

		reordered := []grovev1beta1.PodCliqueSetReplicaUpdateProgress{recorded[1], recorded[0]}
		assert.True(t, e.ClearIfSatisfied(objectMeta, reordered))
	})

	t.Run("mismatched selection does not clear", func(t *testing.T) {
		e := &CurrentlyUpdating{}
		e.Record(objectMeta, recorded)

		assert.False(t, e.ClearIfSatisfied(objectMeta, []grovev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}}))
		// The expectation remains, so a later matching check still clears it.
		assert.True(t, e.ClearIfSatisfied(objectMeta, recorded))
	})
}

func TestCurrentlyUpdatingComparesOnlyLifecycleFacts(t *testing.T) {
	objectMeta := currentlyUpdatingTestObjectMeta()
	startedAt := metav1.NewTime(time.Now())
	recorded := []grovev1beta1.PodCliqueSetReplicaUpdateProgress{{
		ReplicaIndex:    2,
		UpdateStartedAt: startedAt,
		Replacement:     &grovev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
	}}

	t.Run("ignores UpdateStartedAt, Message, and InFlightEpochs", func(t *testing.T) {
		e := &CurrentlyUpdating{}
		e.Record(objectMeta, recorded)

		observed := []grovev1beta1.PodCliqueSetReplicaUpdateProgress{{
			ReplicaIndex:    2,
			UpdateStartedAt: metav1.NewTime(startedAt.Add(time.Hour)),
			Message:         new("in progress"),
			InFlightEpochs:  []string{"100"},
			Replacement:     &grovev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
		}}
		assert.True(t, e.ClearIfSatisfied(objectMeta, observed))
	})

	t.Run("distinguishes targetReplicaIndex", func(t *testing.T) {
		e := &CurrentlyUpdating{}
		e.Record(objectMeta, recorded)

		observed := []grovev1beta1.PodCliqueSetReplicaUpdateProgress{{
			ReplicaIndex: 2,
			Replacement:  &grovev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 3},
		}}
		assert.False(t, e.ClearIfSatisfied(objectMeta, observed))
	})

	t.Run("distinguishes canceled", func(t *testing.T) {
		e := &CurrentlyUpdating{}
		e.Record(objectMeta, recorded)

		canceledAt := metav1.Now()
		observed := []grovev1beta1.PodCliqueSetReplicaUpdateProgress{{
			ReplicaIndex: 2,
			Replacement: &grovev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex: 1,
				CanceledAt:         &canceledAt,
			},
		}}
		assert.False(t, e.ClearIfSatisfied(objectMeta, observed))
	})

	t.Run("distinguishes sourceCleanupCompleted", func(t *testing.T) {
		e := &CurrentlyUpdating{}
		e.Record(objectMeta, recorded)

		cleanupAt := metav1.Now()
		observed := []grovev1beta1.PodCliqueSetReplicaUpdateProgress{{
			ReplicaIndex: 2,
			Replacement: &grovev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex:       1,
				SourceCleanupCompletedAt: &cleanupAt,
			},
		}}
		assert.False(t, e.ClearIfSatisfied(objectMeta, observed))
	})

	t.Run("distinguishes ended", func(t *testing.T) {
		e := &CurrentlyUpdating{}
		e.Record(objectMeta, recorded)

		endedAt := metav1.Now()
		observed := []grovev1beta1.PodCliqueSetReplicaUpdateProgress{{
			ReplicaIndex:  2,
			UpdateEndedAt: &endedAt,
			Replacement:   &grovev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
		}}
		assert.False(t, e.ClearIfSatisfied(objectMeta, observed))
	})

	t.Run("distinguishes hasReplacement", func(t *testing.T) {
		e := &CurrentlyUpdating{}
		e.Record(objectMeta, []grovev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 2}})

		assert.False(t, e.ClearIfSatisfied(objectMeta, recorded))
	})
}

func TestCurrentlyUpdatingForget(t *testing.T) {
	objectMeta := currentlyUpdatingTestObjectMeta()
	e := &CurrentlyUpdating{}
	e.Record(objectMeta, []grovev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}})

	e.Forget(objectMeta)

	// After Forget, any selection is satisfied because no expectation remains.
	assert.True(t, e.ClearIfSatisfied(objectMeta, nil))
}

func currentlyUpdatingTestObjectMeta() metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: "default", Name: "pcs", UID: types.UID("uid")}
}
