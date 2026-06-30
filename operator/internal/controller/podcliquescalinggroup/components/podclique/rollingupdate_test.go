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

package podclique

import (
	"context"
	"testing"
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestCheckAndMarkPCSGCoherentUpdateEnded exercises the PCSG-level coherent close-out gate.
// runSyncFlow already filters callers on IsCoherentUpdateInProgress / IsPCSGUpdateInProgress;
// this function only checks the count conditions and patches UpdateEndedAt.
func TestCheckAndMarkPCSGCoherentUpdateEnded(t *testing.T) {
	const (
		pcsgName = "test-pcsg"
		ns       = "default"
	)
	now := metav1.Time{Time: time.Now()}
	mkPCSG := func(total, updated int32) *grovecorev1alpha1.PodCliqueScalingGroup {
		return &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: pcsgName, Namespace: ns, ResourceVersion: "1"},
			Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
				UpdateProgress: &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{
					UpdateStartedAt:            now,
					PodCliqueSetGenerationHash: "new-hash",
					TotalPodCliquesCount:       total,
					UpdatedPodCliquesCount:     updated,
				},
			},
		}
	}
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

	run := func(_ *testing.T, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) (endedAt *metav1.Time, err error) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pcsg).WithStatusSubresource(pcsg).Build()
		r := _resource{client: cl}
		sc := &syncContext{ctx: context.Background(), pcsg: pcsg}
		err = r.checkAndMarkPCSGCoherentUpdateEnded(logr.Discard(), sc)
		return pcsg.Status.UpdateProgress.UpdateEndedAt, err
	}

	t.Run("TotalPodCliquesCount zero leaves UpdateEndedAt nil (initial state, nothing to roll yet)", func(t *testing.T) {
		endedAt, err := run(t, mkPCSG(0, 0))
		require.NoError(t, err)
		assert.Nil(t, endedAt)
	})

	t.Run("UpdatedPodCliquesCount < TotalPodCliquesCount leaves UpdateEndedAt nil", func(t *testing.T) {
		endedAt, err := run(t, mkPCSG(10, 8))
		require.NoError(t, err)
		assert.Nil(t, endedAt)
	})

	t.Run("all PCLQs updated marks UpdateEndedAt", func(t *testing.T) {
		endedAt, err := run(t, mkPCSG(10, 10))
		// markRollingUpdateEnd patches UpdateEndedAt and then deliberately returns
		// ErrCodeContinueReconcileAndRequeue. The status patch is already applied;
		// the requeue gives reconcileStatus a chance to run on the next pass and
		// advance pcsg.Status.CurrentPodCliqueSetGenerationHash now that
		// IsPCSGUpdateInProgress is false.
		require.True(t, ctrlutils.ShouldContinueReconcileAndRequeue(err), "expected continue-and-requeue signal, got %v", err)
		require.NotNil(t, endedAt)
	})
}
