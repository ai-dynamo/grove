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
	"time"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestCheckDeletionConditions(t *testing.T) {
	tests := []struct {
		name                  string
		clusterTopologyName   string
		topologyConfig        configv1alpha1.ClusterTopologyConfiguration
		existingPodCliqueSets []client.Object
		wantRequeue           bool
		wantError             bool
	}{
		{
			name:                "happy path - no blockers",
			clusterTopologyName: "test-topology",
			topologyConfig: configv1alpha1.ClusterTopologyConfiguration{
				Enabled: false,
			},
			existingPodCliqueSets: []client.Object{},
			wantRequeue:           false,
			wantError:             false,
		},
		{
			name:                "blocked by single PodCliqueSet reference",
			clusterTopologyName: "test-topology",
			topologyConfig: configv1alpha1.ClusterTopologyConfiguration{
				Enabled: false,
			},
			existingPodCliqueSets: []client.Object{
				testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					WithTopologyLabel("test-topology").
					Build(),
			},
			wantRequeue: false,
			wantError:   false,
		},
		{
			name:                "blocked by multiple PodCliqueSet references",
			clusterTopologyName: "test-topology",
			topologyConfig: configv1alpha1.ClusterTopologyConfiguration{
				Enabled: false,
			},
			existingPodCliqueSets: []client.Object{
				testutils.NewPodCliqueSetBuilder("test-pcs-1", "test-ns", uuid.NewUUID()).
					WithTopologyLabel("test-topology").
					Build(),
				testutils.NewPodCliqueSetBuilder("test-pcs-2", "test-ns", uuid.NewUUID()).
					WithTopologyLabel("test-topology").
					Build(),
				testutils.NewPodCliqueSetBuilder("test-pcs-3", "other-ns", uuid.NewUUID()).
					WithTopologyLabel("test-topology").
					Build(),
			},
			wantRequeue: false,
			wantError:   false,
		},
		{
			name:                "blocked by topology enabled with default name",
			clusterTopologyName: configv1alpha1.DefaultClusterTopologyName,
			topologyConfig: configv1alpha1.ClusterTopologyConfiguration{
				Enabled: true,
			},
			existingPodCliqueSets: []client.Object{},
			wantRequeue:           false,
			wantError:             false,
		},
		{
			name:                "blocked by topology enabled with default name that is specified explicitly",
			clusterTopologyName: configv1alpha1.DefaultClusterTopologyName,
			topologyConfig: configv1alpha1.ClusterTopologyConfiguration{
				Enabled: true,
				Name:    configv1alpha1.DefaultClusterTopologyName,
			},
			existingPodCliqueSets: []client.Object{},
			wantRequeue:           false,
			wantError:             false,
		},
		{
			name:                "blocked by topology enabled with custom name",
			clusterTopologyName: "custom-topology",
			topologyConfig: configv1alpha1.ClusterTopologyConfiguration{
				Enabled: true,
				Name:    "custom-topology",
			},
			existingPodCliqueSets: []client.Object{},
			wantRequeue:           false,
			wantError:             false,
		},
		{
			name:                "allowed when topology enabled but different name",
			clusterTopologyName: configv1alpha1.DefaultClusterTopologyName,
			topologyConfig: configv1alpha1.ClusterTopologyConfiguration{
				Enabled: true,
				Name:    "other-topology",
			},
			existingPodCliqueSets: []client.Object{},
			wantRequeue:           false,
			wantError:             false,
		},
		{
			name:                "topology disabled - no references",
			clusterTopologyName: "test-topology",
			topologyConfig: configv1alpha1.ClusterTopologyConfiguration{
				Enabled: false,
			},
			existingPodCliqueSets: []client.Object{},
			wantRequeue:           false,
			wantError:             false,
		},
		{
			name:                "PodCliqueSet with different topology label - allowed",
			clusterTopologyName: "test-topology",
			topologyConfig: configv1alpha1.ClusterTopologyConfiguration{
				Enabled: false,
			},
			existingPodCliqueSets: []client.Object{
				testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					WithTopologyLabel("different-topology").
					Build(),
			},
			wantRequeue: false,
			wantError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.topologyConfig.Name == "" {
				tt.topologyConfig.Name = configv1alpha1.DefaultClusterTopologyName // TODO remove this after taking care of registering the defaults properly
			}

			ct := testutils.NewSimpleClusterTopology(tt.clusterTopologyName)
			allObjects := append([]client.Object{ct}, tt.existingPodCliqueSets...)
			fakeClient := testutils.SetupFakeClient(allObjects...)
			reconciler := &Reconciler{
				client:        fakeClient,
				eventRecorder: record.NewFakeRecorder(10),
				config: configv1alpha1.OperatorConfiguration{
					ClusterTopology: tt.topologyConfig,
				},
			}

			// Run checkDeletionConditions
			stepResult := reconciler.checkDeletionConditions(context.Background(), logr.Discard(), ct)
			result, err := stepResult.Result()

			if tt.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.wantRequeue {
				assert.True(t, result.RequeueAfter > 0, "expected requeue after delay")
			} else {
				assert.False(t, result.Requeue, "should not requeue immediately")
				assert.Equal(t, time.Duration(0), result.RequeueAfter, "should not requeue after delay")
			}
		})
	}
}

func TestRemoveFinalizer(t *testing.T) {
	tests := []struct {
		name      string
		ct        *testutils.ClusterTopologyBuilder
		wantError bool
	}{
		{
			name:      "successful removal",
			ct:        testutils.NewClusterTopologyBuilder("test-topology").WithFinalizer(constants.FinalizerClusterTopology),
			wantError: false,
		},
		{
			name:      "finalizer already absent - no-op",
			ct:        testutils.NewClusterTopologyBuilder("test-topology"),
			wantError: false,
		},
		{
			name: "multiple finalizers - remove only ClusterTopology finalizer",
			ct: testutils.NewClusterTopologyBuilder("test-topology").
				WithFinalizer(constants.FinalizerClusterTopology).
				WithFinalizer("other.finalizer/test"),
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ct := tt.ct.Build()
			fakeClient := testutils.SetupFakeClient(ct)
			reconciler := &Reconciler{client: fakeClient, eventRecorder: record.NewFakeRecorder(10)}

			hadFinalizer := controllerutil.ContainsFinalizer(ct, constants.FinalizerClusterTopology)

			// Run removeFinalizer
			stepResult := reconciler.removeFinalizer(context.Background(), logr.Discard(), ct)
			_, err := stepResult.Result()

			if tt.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Verify finalizer was removed
			updatedCT := tt.ct.Build()
			getErr := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(ct), updatedCT)
			require.NoError(t, getErr)

			hasFinalizer := controllerutil.ContainsFinalizer(updatedCT, constants.FinalizerClusterTopology)

			if hadFinalizer {
				assert.False(t, hasFinalizer, "finalizer should have been removed")
			} else {
				assert.False(t, hasFinalizer, "finalizer should remain absent")
			}

			// For multiple finalizers case, verify other finalizers remain
			if tt.name == "multiple finalizers - remove only ClusterTopology finalizer" {
				assert.True(t, controllerutil.ContainsFinalizer(updatedCT, "other.finalizer/test"), "other finalizer should remain")
			}
		})
	}
}

func TestTriggerDeletionFlow(t *testing.T) {
	tests := []struct {
		name                 string
		ct                   *testutils.ClusterTopologyBuilder
		topologyConfig       configv1alpha1.ClusterTopologyConfiguration
		existingPCS          []client.Object
		wantFinalizerRemoved bool
		wantRequeue          bool
		wantError            bool
	}{
		{
			name: "successful deletion flow - no blockers",
			ct: testutils.NewClusterTopologyBuilder("test-topology").
				WithFinalizer(constants.FinalizerClusterTopology),
			topologyConfig: configv1alpha1.ClusterTopologyConfiguration{
				Enabled: false,
			},
			existingPCS:          []client.Object{},
			wantFinalizerRemoved: true,
			wantRequeue:          false,
			wantError:            false,
		},
		{
			name: "blocked deletion flow - PodCliqueSet reference",
			ct: testutils.NewClusterTopologyBuilder("test-topology").
				WithFinalizer(constants.FinalizerClusterTopology),
			topologyConfig: configv1alpha1.ClusterTopologyConfiguration{
				Enabled: false,
			},
			existingPCS: []client.Object{
				testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					WithTopologyLabel("test-topology").
					Build(),
			},
			wantFinalizerRemoved: false,
			wantRequeue:          false,
			wantError:            false,
		},
		{
			name: "blocked deletion flow - topology enabled",
			ct: testutils.NewClusterTopologyBuilder(configv1alpha1.DefaultClusterTopologyName).
				WithFinalizer(constants.FinalizerClusterTopology),
			topologyConfig: configv1alpha1.ClusterTopologyConfiguration{
				Enabled: true,
				Name:    configv1alpha1.DefaultClusterTopologyName, // TODO remove when defaults are registered properly
			},
			existingPCS:          []client.Object{},
			wantFinalizerRemoved: false,
			wantRequeue:          false,
			wantError:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ct := tt.ct.Build()
			allObjects := append([]client.Object{ct}, tt.existingPCS...)
			fakeClient := testutils.SetupFakeClient(allObjects...)
			reconciler := &Reconciler{
				client:        fakeClient,
				eventRecorder: record.NewFakeRecorder(10),
				config: configv1alpha1.OperatorConfiguration{
					ClusterTopology: tt.topologyConfig,
				},
			}

			// Run triggerDeletionFlow
			stepResult := reconciler.triggerDeletionFlow(context.Background(), logr.Discard(), ct)
			result, err := stepResult.Result()

			if tt.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.wantRequeue {
				assert.True(t, result.RequeueAfter > 0, "should requeue")
			} else {
				assert.False(t, result.Requeue, "should not requeue immediately")
				assert.Equal(t, time.Duration(0), result.RequeueAfter, "should not requeue after delay")
			}

			// Verify finalizer state
			updatedCT := tt.ct.Build()
			getErr := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(ct), updatedCT)
			require.NoError(t, getErr)

			hasFinalizer := controllerutil.ContainsFinalizer(updatedCT, constants.FinalizerClusterTopology)

			if tt.wantFinalizerRemoved {
				assert.False(t, hasFinalizer, "finalizer should have been removed")
			} else {
				assert.True(t, hasFinalizer, "finalizer should be retained")
			}
		})
	}
}
