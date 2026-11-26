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
	"context"
	"testing"

	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	pcsexpectations "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/expectations"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/sync"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
)

// TestTriggerDeletionFlowClearsGenerationStatusExpectation verifies the deletion flow removes the PCS
// generation-status expectation from the tracker.
func TestTriggerDeletionFlowClearsGenerationStatusExpectation(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid-del").Build()
	pcs.Finalizers = []string{apiconstants.FinalizerPodCliqueSet}

	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{
		client:                        fakeClient,
		operatorRegistry:              component.NewOperatorRegistry[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result](),
		reconcileStatusRecorder:       ctrlcommon.NewReconcileErrorRecorder(fakeClient),
		scalingProgressExpectations:   &pcsexpectations.ScalingProgress{},
		currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{},
	}
	r.pcsGenerationStatusExpectations.Record(pcs.ObjectMeta, "some-hash", nil)

	r.triggerDeletionFlow(context.Background(), logr.Discard(), pcs)

	assert.True(t, r.pcsGenerationStatusExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil, nil), "generation-hash expectation should be cleared when the PCS is deleted")
}
