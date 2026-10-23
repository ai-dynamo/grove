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

package podcliqueset

import (
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/sync"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestEnsureScalingProgressExpectationSatisfied(t *testing.T) {
	objectMeta := metav1.ObjectMeta{Namespace: "default", Name: "pcs", UID: types.UID("uid")}
	expectedProgress := &grovecorev1alpha1.PodCliqueSetScalingProgress{ScaleOutReplicaIndices: []int32{1}}
	expectations := &pcssync.ScalingProgressExpectations{}
	expectations.Record(objectMeta, expectedProgress)
	reconciler := &Reconciler{scalingProgressExpectations: expectations}
	pcs := &grovecorev1alpha1.PodCliqueSet{ObjectMeta: objectMeta}

	result := reconciler.ensureScalingProgressExpectationSatisfied(pcs)
	assert.True(t, result.NeedsRequeue())

	pcs.Status.ScalingProgress = expectedProgress
	result = reconciler.ensureScalingProgressExpectationSatisfied(pcs)
	assert.False(t, result.NeedsRequeue())
	assert.False(t, ctrlcommon.ShortCircuitReconcileFlow(result))

	pcs.Status.ScalingProgress = nil
	result = reconciler.ensureScalingProgressExpectationSatisfied(pcs)
	assert.False(t, result.NeedsRequeue())
	assert.False(t, ctrlcommon.ShortCircuitReconcileFlow(result))
}
