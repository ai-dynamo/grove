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

package podgang

import (
	"testing"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSetInitializedCondition(t *testing.T) {
	pg := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{Name: "pg-1", Namespace: "default", Generation: 1},
	}
	setInitializedCondition(pg, metav1.ConditionFalse, "PodsPending", "waiting")
	require.Len(t, pg.Status.Conditions, 1)
	assert.Equal(t, string(groveschedulerv1alpha1.PodGangConditionTypeInitialized), pg.Status.Conditions[0].Type)
	assert.Equal(t, metav1.ConditionFalse, pg.Status.Conditions[0].Status)
	assert.Equal(t, "PodsPending", pg.Status.Conditions[0].Reason)
	assert.Equal(t, "waiting", pg.Status.Conditions[0].Message)

	// Update existing condition to ready
	setInitializedCondition(pg, metav1.ConditionTrue, "Ready", "all ready")
	require.Len(t, pg.Status.Conditions, 1)
	assert.Equal(t, metav1.ConditionTrue, pg.Status.Conditions[0].Status)
	assert.Equal(t, "Ready", pg.Status.Conditions[0].Reason)
}

func TestHasInitializedCondition(t *testing.T) {
	pg := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{Name: "pg-1", Namespace: "default"},
	}
	assert.False(t, hasInitializedCondition(pg))

	setInitializedCondition(pg, metav1.ConditionFalse, "PodsPending", "waiting")
	assert.True(t, hasInitializedCondition(pg))
}
