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

package clustertopology

import (
	"context"
	"testing"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestEnsureFinalizer(t *testing.T) {
	tests := []struct {
		name        string
		ct          *testutils.ClusterTopologyBuilder
		wantError   bool
		wantPresent bool
	}{
		{
			name:        "add finalizer to new resource",
			ct:          testutils.NewClusterTopologyBuilder("test-topology"),
			wantError:   false,
			wantPresent: true,
		},
		{
			name:        "finalizer already present - idempotent",
			ct:          testutils.NewClusterTopologyBuilder("test-topology").WithFinalizer(constants.FinalizerClusterTopology),
			wantError:   false,
			wantPresent: true,
		},
		{
			name:        "multiple reconciliations - idempotent",
			ct:          testutils.NewClusterTopologyBuilder("test-topology"),
			wantError:   false,
			wantPresent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ct := tt.ct.Build()
			fakeClient := testutils.SetupFakeClient(ct)
			reconciler := &Reconciler{client: fakeClient}

			// Run ensureFinalizer
			stepResult := reconciler.ensureFinalizer(context.Background(), logr.Discard(), ct)

			if tt.wantError {
				assert.True(t, stepResult.HasErrors(), "expected errors")
			} else {
				assert.False(t, stepResult.HasErrors(), "expected no errors")
			}

			// Verify finalizer presence
			updatedCT := tt.ct.Build()
			getErr := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(ct), updatedCT)
			require.NoError(t, getErr)

			hasFinalizer := controllerutil.ContainsFinalizer(updatedCT, constants.FinalizerClusterTopology)
			assert.Equal(t, tt.wantPresent, hasFinalizer, "finalizer presence mismatch")

			// Test idempotency - run again
			if !tt.wantError && tt.name == "multiple reconciliations - idempotent" {
				stepResult2 := reconciler.ensureFinalizer(context.Background(), logr.Discard(), updatedCT)
				assert.False(t, stepResult2.HasErrors(), "expected no errors on second run")

				updatedCT2 := tt.ct.Build()
				getErr2 := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(ct), updatedCT2)
				require.NoError(t, getErr2)
				assert.True(t, controllerutil.ContainsFinalizer(updatedCT2, constants.FinalizerClusterTopology))
			}
		})
	}
}

func TestReconcileSpec(t *testing.T) {
	tests := []struct {
		name               string
		ct                 *testutils.ClusterTopologyBuilder
		wantFinalizerAdded bool
		wantRequeue        bool
		wantError          bool
	}{
		{
			name:               "new ClusterTopology - add finalizer",
			ct:                 testutils.NewClusterTopologyBuilder("test-topology"),
			wantFinalizerAdded: true,
			wantRequeue:        false,
			wantError:          false,
		},
		{
			name:               "existing ClusterTopology with finalizer - no-op",
			ct:                 testutils.NewClusterTopologyBuilder("test-topology").WithFinalizer(constants.FinalizerClusterTopology),
			wantFinalizerAdded: true,
			wantRequeue:        false,
			wantError:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ct := tt.ct.Build()
			fakeClient := testutils.SetupFakeClient(ct)
			reconciler := &Reconciler{client: fakeClient}

			// Run reconcileSpec
			stepResult := reconciler.reconcileSpec(context.Background(), logr.Discard(), ct)
			result, err := stepResult.Result()

			if tt.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tt.wantRequeue, result.Requeue, "requeue mismatch")
			assert.Equal(t, tt.wantRequeue, result.RequeueAfter > 0, "requeue after mismatch")

			// Verify finalizer was added if expected
			updatedCT := tt.ct.Build()
			getErr := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(ct), updatedCT)
			require.NoError(t, getErr)

			hasFinalizer := controllerutil.ContainsFinalizer(updatedCT, constants.FinalizerClusterTopology)
			assert.Equal(t, tt.wantFinalizerAdded, hasFinalizer, "finalizer presence mismatch")
		})
	}
}
