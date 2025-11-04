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
	"net/http"
	"testing"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

func TestReconcile(t *testing.T) {
	tests := []struct {
		name                 string
		clusterTopology      *grovecorev1alpha1.ClusterTopology
		topologyConfig       configv1alpha1.TopologyConfiguration
		existingObjects      []client.Object
		wantRequeue          bool
		wantError            bool
		wantFinalizerPresent bool
		resourceExists       bool
	}{
		{
			name:            "new ClusterTopology creation - add finalizer",
			clusterTopology: testutils.NewSimpleClusterTopology("test-topology"),
			topologyConfig: configv1alpha1.TopologyConfiguration{
				Enabled: false,
			},
			existingObjects:      []client.Object{},
			wantRequeue:          false,
			wantError:            false,
			wantFinalizerPresent: true,
			resourceExists:       true,
		},
		{
			name:            "existing ClusterTopology with finalizer - no-op",
			clusterTopology: testutils.NewClusterTopologyWithFinalizer("test-topology"),
			topologyConfig: configv1alpha1.TopologyConfiguration{
				Enabled: false,
			},
			existingObjects:      []client.Object{},
			wantRequeue:          false,
			wantError:            false,
			wantFinalizerPresent: true,
			resourceExists:       true,
		},
		{
			name:            "ClusterTopology deletion allowed - no blockers",
			clusterTopology: testutils.NewClusterTopologyMarkedForDeletion("test-topology", true),
			topologyConfig: configv1alpha1.TopologyConfiguration{
				Enabled: false,
			},
			existingObjects:      []client.Object{},
			wantRequeue:          false,
			wantError:            false,
			wantFinalizerPresent: false,
			resourceExists:       true,
		},
		{
			name:            "ClusterTopology deletion blocked by PodCliqueSet",
			clusterTopology: testutils.NewClusterTopologyMarkedForDeletion("test-topology", true),
			topologyConfig: configv1alpha1.TopologyConfiguration{
				Enabled: false,
			},
			existingObjects: []client.Object{
				testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					WithTopologyLabel("test-topology").
					Build(),
			},
			wantRequeue:          true,
			wantError:            false,
			wantFinalizerPresent: true,
			resourceExists:       true,
		},
		{
			name:            "ClusterTopology deletion blocked by topology config",
			clusterTopology: testutils.NewClusterTopologyMarkedForDeletion("grove-topology", true),
			topologyConfig: configv1alpha1.TopologyConfiguration{
				Enabled: true,
				Name:    nil, // defaults to grove-topology
			},
			existingObjects:      []client.Object{},
			wantRequeue:          true,
			wantError:            false,
			wantFinalizerPresent: true,
			resourceExists:       true,
		},
		{
			name: "finalizer already removed - immediate success",
			// Create object with deletion timestamp but WITH a dummy finalizer to satisfy fake client
			// Then test verifies that if our finalizer is already removed, we return immediately
			clusterTopology: func() *grovecorev1alpha1.ClusterTopology {
				ct := testutils.NewClusterTopologyBuilder("test-topology").
					WithFinalizer("other.finalizer/test").
					WithDeletionTimestamp(metav1.Now()).
					Build()
				return ct
			}(),
			topologyConfig: configv1alpha1.TopologyConfiguration{
				Enabled: false,
			},
			existingObjects:      []client.Object{},
			wantRequeue:          false,
			wantError:            false,
			wantFinalizerPresent: false,
			resourceExists:       true,
		},
		{
			name:                 "ClusterTopology not found - NotFound ignored",
			clusterTopology:      testutils.NewSimpleClusterTopology("non-existent"),
			topologyConfig:       configv1alpha1.TopologyConfiguration{Enabled: false},
			existingObjects:      []client.Object{},
			wantRequeue:          false,
			wantError:            false,
			wantFinalizerPresent: false,
			resourceExists:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var allObjects []client.Object
			if tt.resourceExists {
				allObjects = append([]client.Object{tt.clusterTopology}, tt.existingObjects...)
			} else {
				allObjects = tt.existingObjects
			}

			fakeClient := testutils.SetupFakeClient(allObjects...)
			reconciler := &Reconciler{
				client:                  fakeClient,
				config:                  tt.topologyConfig,
				reconcileStatusRecorder: ctrlcommon.NewReconcileErrorRecorder(fakeClient),
			}

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: tt.clusterTopology.Name,
				},
			}

			// Run Reconcile
			result, err := reconciler.Reconcile(context.Background(), req)

			if tt.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.wantRequeue {
				assert.True(t, result.Requeue || result.RequeueAfter > 0, "should requeue")
			} else {
				assert.False(t, result.Requeue, "should not requeue")
				assert.Equal(t, ctrl.Result{}, result, "result should be empty")
			}

			// Verify finalizer state if resource exists
			if tt.resourceExists && tt.name != "ClusterTopology not found - NotFound ignored" {
				updatedCT := &grovecorev1alpha1.ClusterTopology{}
				err = fakeClient.Get(context.Background(), client.ObjectKeyFromObject(tt.clusterTopology), updatedCT)

				// If finalizer was expected to be removed (successful deletion), resource may be gone
				if !tt.wantFinalizerPresent && client.IgnoreNotFound(err) == nil {
					// Resource was deleted or not found - this is expected for successful deletion
					if err != nil {
						// Resource was deleted - finalizer was successfully removed
						return
					}
				} else {
					require.NoError(t, err, "unexpected error getting ClusterTopology")
				}

				hasFinalizer := controllerutil.ContainsFinalizer(updatedCT, constants.FinalizerClusterTopology)
				assert.Equal(t, tt.wantFinalizerPresent, hasFinalizer, "finalizer presence mismatch")
			}
		})
	}
}

func TestNewReconciler(t *testing.T) {
	tests := []struct {
		name           string
		topologyConfig configv1alpha1.TopologyConfiguration
	}{
		{
			name: "topology disabled",
			topologyConfig: configv1alpha1.TopologyConfiguration{
				Enabled: false,
			},
		},
		{
			name: "topology enabled with default name",
			topologyConfig: configv1alpha1.TopologyConfiguration{
				Enabled: true,
				Name:    nil,
			},
		},
		{
			name: "topology enabled with custom name",
			topologyConfig: configv1alpha1.TopologyConfiguration{
				Enabled: true,
				Name:    ptr.To("custom-topology"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a fake manager
			fakeClient := testutils.SetupFakeClient()
			mgr := &fakeManager{client: fakeClient}

			// Create reconciler
			reconciler := NewReconciler(mgr, tt.topologyConfig)

			// Verify all fields initialized
			assert.NotNil(t, reconciler.client, "client should be initialized")
			assert.NotNil(t, reconciler.reconcileStatusRecorder, "status recorder should be initialized")

			// Verify topology config passed correctly
			assert.Equal(t, tt.topologyConfig.Enabled, reconciler.config.Enabled, "topology enabled mismatch")
			if tt.topologyConfig.Name != nil {
				require.NotNil(t, reconciler.config.Name, "topology name should not be nil")
				assert.Equal(t, *tt.topologyConfig.Name, *reconciler.config.Name, "topology name mismatch")
			} else {
				assert.Nil(t, reconciler.config.Name, "topology name should be nil")
			}
		})
	}
}

// fakeManager is a minimal implementation of ctrl.Manager for testing
type fakeManager struct {
	client client.Client
}

func (m *fakeManager) GetClient() client.Client {
	return m.client
}

func (m *fakeManager) GetScheme() *runtime.Scheme {
	return nil
}

func (m *fakeManager) GetConfig() *rest.Config {
	return nil
}

func (m *fakeManager) GetEventRecorderFor(name string) record.EventRecorder {
	return nil
}

func (m *fakeManager) GetRESTMapper() meta.RESTMapper {
	return nil
}

func (m *fakeManager) GetAPIReader() client.Reader {
	return nil
}

func (m *fakeManager) GetWebhookServer() webhook.Server {
	return nil
}

func (m *fakeManager) GetLogger() logr.Logger {
	return logr.Discard()
}

func (m *fakeManager) GetControllerOptions() config.Controller {
	return config.Controller{}
}

func (m *fakeManager) Add(manager.Runnable) error {
	return nil
}

func (m *fakeManager) Elected() <-chan struct{} {
	return nil
}

func (m *fakeManager) AddMetricsServerExtraHandler(path string, handler http.Handler) error {
	return nil
}

func (m *fakeManager) AddHealthzCheck(name string, check healthz.Checker) error {
	return nil
}

func (m *fakeManager) AddReadyzCheck(name string, check healthz.Checker) error {
	return nil
}

func (m *fakeManager) Start(ctx context.Context) error {
	return nil
}

func (m *fakeManager) GetHTTPClient() *http.Client {
	return nil
}

func (m *fakeManager) GetCache() cache.Cache {
	return nil
}

func (m *fakeManager) GetFieldIndexer() client.FieldIndexer {
	return nil
}

func (m *fakeManager) GetControllerOf(schema.GroupVersionKind) (string, error) {
	return "", nil
}

func (m *fakeManager) GetRESTClient() (*rest.RESTClient, error) {
	return nil, nil
}
