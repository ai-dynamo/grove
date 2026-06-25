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
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"
	"time"

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/kai"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	kaitopologyv1alpha1 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/kai/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const topologyName = "test-topology"

func newKaiBackends(cl client.Client) map[string]scheduler.TopologyAwareBackend {
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}
	b := kai.New(cl, cl.Scheme(), nil, profile)
	return map[string]scheduler.TopologyAwareBackend{b.Name(): b.(scheduler.TopologyAwareBackend)}
}

func TestSynchronizeTopologyListsAndSyncs(t *testing.T) {
	ctx := context.Background()
	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	ct := createTestClusterTopology(topologyName, topologyLevels)

	cl := testutils.CreateDefaultFakeClient([]client.Object{ct})
	logger := logr.Discard()

	err := SynchronizeTopology(ctx, cl, logger, newKaiBackends(cl))
	require.NoError(t, err)

	// Verify KAI Topology was created
	fetchedKAITopology := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(ctx, client.ObjectKey{Name: topologyName}, fetchedKAITopology)
	require.NoError(t, err)
	assert.Len(t, fetchedKAITopology.Spec.Levels, 2)
	assert.Equal(t, "topology.kubernetes.io/zone", fetchedKAITopology.Spec.Levels[0].NodeLabel)
	assert.Equal(t, "kubernetes.io/hostname", fetchedKAITopology.Spec.Levels[1].NodeLabel)
	assert.True(t, metav1.IsControlledBy(fetchedKAITopology, ct))
}

func TestSynchronizeTopologyMultipleCTs(t *testing.T) {
	ctx := context.Background()
	ct1 := createTestClusterTopology("topology-a", []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
	})
	ct2 := createTestClusterTopology("topology-b", []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	})

	cl := testutils.CreateDefaultFakeClient([]client.Object{ct1, ct2})
	logger := logr.Discard()

	err := SynchronizeTopology(ctx, cl, logger, newKaiBackends(cl))
	require.NoError(t, err)

	// Verify both KAI Topologies were created
	kai1 := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(ctx, client.ObjectKey{Name: "topology-a"}, kai1)
	require.NoError(t, err)
	assert.Len(t, kai1.Spec.Levels, 1)

	kai2 := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(ctx, client.ObjectKey{Name: "topology-b"}, kai2)
	require.NoError(t, err)
	assert.Len(t, kai2.Spec.Levels, 1)
}

func TestSynchronizeTopologyNoCTs(t *testing.T) {
	ctx := context.Background()
	cl := testutils.CreateDefaultFakeClient(nil)
	logger := logr.Discard()

	err := SynchronizeTopology(ctx, cl, logger, newKaiBackends(cl))
	require.NoError(t, err)
}

func TestSynchronizeTopologyNoTASBackends(t *testing.T) {
	ctx := context.Background()
	ct := createTestClusterTopology(topologyName, []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	})
	cl := testutils.CreateDefaultFakeClient([]client.Object{ct})
	logger := logr.Discard()

	// Pass nil backends
	err := SynchronizeTopology(ctx, cl, logger, nil)
	require.NoError(t, err)
}

func TestSynchronizeTopologySkipsExternallyManaged(t *testing.T) {
	ctx := context.Background()
	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	ct := createTestClusterTopology(topologyName, topologyLevels)
	ct.Spec.SchedulerTopologyBindings = []grovecorev1alpha1.SchedulerTopologyBinding{
		{SchedulerName: "kai-scheduler", TopologyReference: "external-kai-topology"},
	}

	cl := testutils.CreateDefaultFakeClient([]client.Object{ct})
	logger := logr.Discard()

	// KAI backend is listed in schedulerTopologyReferences — SyncTopology should NOT be called.
	// If it were called, it would try to create a KAI Topology and succeed, so we verify
	// that no KAI Topology was created.
	err := SynchronizeTopology(ctx, cl, logger, newKaiBackends(cl))
	require.NoError(t, err)

	kaiTopology := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(ctx, client.ObjectKey{Name: topologyName}, kaiTopology)
	assert.True(t, apierrors.IsNotFound(err), "KAI Topology should not have been created for externally-managed CT")
}

func TestSynchronizeTopologyListError(t *testing.T) {
	ctx := context.Background()
	listErr := apierrors.NewInternalError(assert.AnError)
	ctListGVK := grovecorev1alpha1.SchemeGroupVersion.WithKind("ClusterTopologyBindingList")
	cl := testutils.NewTestClientBuilder().
		RecordErrorForObjectsMatchingLabels(testutils.ClientMethodList, client.ObjectKey{}, ctListGVK, nil, listErr).
		Build()
	logger := logr.Discard()

	err := SynchronizeTopology(ctx, cl, logger, newKaiBackends(cl))
	assert.Error(t, err)
}

func TestGetClusterTopologyLevels(t *testing.T) {
	tests := []struct {
		name              string
		clusterTopology   *grovecorev1alpha1.ClusterTopologyBinding
		topologyName      string
		getError          *apierrors.StatusError
		expectedLevels    []grovecorev1alpha1.TopologyLevel
		expectError       bool
		expectedErrorType error
	}{
		{
			name: "successfully retrieve topology levels",
			clusterTopology: &grovecorev1alpha1.ClusterTopologyBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-topology",
				},
				Spec: grovecorev1alpha1.ClusterTopologyBindingSpec{
					Levels: []grovecorev1alpha1.TopologyLevel{
						{
							Domain: grovecorev1alpha1.TopologyDomainRegion,
							Key:    "topology.kubernetes.io/region",
						},
						{
							Domain: grovecorev1alpha1.TopologyDomainZone,
							Key:    "topology.kubernetes.io/zone",
						},
						{
							Domain: grovecorev1alpha1.TopologyDomainHost,
							Key:    "kubernetes.io/hostname",
						},
					},
				},
			},
			topologyName: "test-topology",
			expectedLevels: []grovecorev1alpha1.TopologyLevel{
				{
					Domain: grovecorev1alpha1.TopologyDomainRegion,
					Key:    "topology.kubernetes.io/region",
				},
				{
					Domain: grovecorev1alpha1.TopologyDomainZone,
					Key:    "topology.kubernetes.io/zone",
				},
				{
					Domain: grovecorev1alpha1.TopologyDomainHost,
					Key:    "kubernetes.io/hostname",
				},
			},
			expectError: false,
		},
		{
			name: "topology not found",
			clusterTopology: &grovecorev1alpha1.ClusterTopologyBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "existing-topology",
				},
				Spec: grovecorev1alpha1.ClusterTopologyBindingSpec{
					Levels: []grovecorev1alpha1.TopologyLevel{
						{
							Domain: grovecorev1alpha1.TopologyDomainRegion,
							Key:    "topology.kubernetes.io/region",
						},
					},
				},
			},
			topologyName: "non-existent-topology",
			getError: apierrors.NewNotFound(
				schema.GroupResource{Group: apicommonconstants.OperatorGroupName, Resource: "clustertopologybindings"},
				"non-existent-topology",
			),
			expectError:       true,
			expectedErrorType: &apierrors.StatusError{},
		},
		{
			name: "client Get returns error",
			clusterTopology: &grovecorev1alpha1.ClusterTopologyBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-topology",
				},
				Spec: grovecorev1alpha1.ClusterTopologyBindingSpec{
					Levels: []grovecorev1alpha1.TopologyLevel{
						{
							Domain: grovecorev1alpha1.TopologyDomainZone,
							Key:    "topology.kubernetes.io/zone",
						},
					},
				},
			},
			topologyName:      "test-topology",
			getError:          apierrors.NewInternalError(assert.AnError),
			expectError:       true,
			expectedErrorType: &apierrors.StatusError{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Create fake client
			var existingObjects []client.Object
			if test.clusterTopology != nil {
				existingObjects = append(existingObjects, test.clusterTopology)
			}
			clientBuilder := testutils.NewTestClientBuilder().WithObjects(existingObjects...)
			// Record error if specified
			if test.getError != nil {
				clientBuilder.RecordErrorForObjects(
					testutils.ClientMethodGet,
					test.getError,
					client.ObjectKey{Name: test.topologyName},
				)
			}
			fakeClient := clientBuilder.Build()

			// Call the function
			topologLevels, err := GetClusterTopologyLevels(context.Background(), fakeClient, test.topologyName)

			// Validate results
			if test.expectError {
				require.Error(t, err)
				if test.expectedErrorType != nil {
					assert.IsType(t, test.expectedErrorType, err)
				}
				assert.Nil(t, topologLevels)
			} else {
				require.NoError(t, err)
				assert.Equal(t, test.expectedLevels, topologLevels)
			}
		})
	}
}

// Helper functions for creating test resources
// --------------------------------------------------
// createTestClusterTopology creates a ClusterTopologyBinding with the given name and topology levels.
func createTestClusterTopology(name string, levels []grovecorev1alpha1.TopologyLevel) *grovecorev1alpha1.ClusterTopologyBinding {
	return &grovecorev1alpha1.ClusterTopologyBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			UID:  uuid.NewUUID(),
		},
		Spec: grovecorev1alpha1.ClusterTopologyBindingSpec{
			Levels: levels,
		},
	}
}

// transientListClient wraps a client.Client and returns failErr for the first failCount List calls,
// then delegates all subsequent calls to the underlying client. Used to simulate transient API errors.
type transientListClient struct {
	client.Client
	failCount int
	callCount int
	failErr   error
}

func (c *transientListClient) List(ctx context.Context, objList client.ObjectList, opts ...client.ListOption) error {
	c.callCount++
	if c.callCount <= c.failCount {
		return c.failErr
	}
	return c.Client.List(ctx, objList, opts...)
}

// fastBackoff is a zero-duration backoff suitable for unit tests to avoid slowing them down.
var fastBackoff = wait.Backoff{Duration: time.Millisecond, Factor: 1.0, Steps: 10}

func TestSynchronizeTopologyWithRetry_SucceedsAfterTransientError(t *testing.T) {
	ctx := context.Background()
	ct := createTestClusterTopology(topologyName, []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	})
	base := testutils.CreateDefaultFakeClient([]client.Object{ct})
	// Inject a transient 500 error for the first 2 List calls; the 3rd call succeeds.
	cl := &transientListClient{
		Client:    base,
		failCount: 2,
		failErr:   apierrors.NewInternalError(assert.AnError),
	}

	err := SynchronizeTopologyWithRetry(ctx, cl, logr.Discard(), newKaiBackends(base), fastBackoff)
	require.NoError(t, err)
	assert.Equal(t, 3, cl.callCount, "expected 2 transient failures then 1 success")

	// Verify topology was still created correctly.
	kaiTopology := &kaitopologyv1alpha1.Topology{}
	require.NoError(t, base.Get(ctx, client.ObjectKey{Name: topologyName}, kaiTopology))
	assert.Len(t, kaiTopology.Spec.Levels, 1)
}

func TestSynchronizeTopologyWithRetry_PermanentErrorNotRetried(t *testing.T) {
	ctx := context.Background()
	permErr := apierrors.NewForbidden(schema.GroupResource{Resource: "clustertopologybindings"}, "", assert.AnError)
	ctListGVK := grovecorev1alpha1.SchemeGroupVersion.WithKind("ClusterTopologyBindingList")
	cl := testutils.NewTestClientBuilder().
		RecordErrorForObjectsMatchingLabels(testutils.ClientMethodList, client.ObjectKey{}, ctListGVK, nil, permErr).
		Build()

	callCount := 0
	// Wrap the permanent-error client in a counter to verify it is only called once.
	countingCl := &transientListClient{Client: cl, failCount: 100, failErr: permErr}

	err := SynchronizeTopologyWithRetry(ctx, countingCl, logr.Discard(), newKaiBackends(cl), fastBackoff)
	require.Error(t, err)
	assert.True(t, apierrors.IsForbidden(err), "expected Forbidden error to propagate")
	// The permanent error must not be retried: fn is called exactly once.
	_ = callCount
	assert.Equal(t, 1, countingCl.callCount, "permanent error should not be retried")
}

func TestSynchronizeTopologyWithRetry_ExhaustsRetriesOnPersistentTransientError(t *testing.T) {
	ctx := context.Background()
	transientErr := apierrors.NewServiceUnavailable("apiserver temporarily unavailable")
	ctListGVK := grovecorev1alpha1.SchemeGroupVersion.WithKind("ClusterTopologyBindingList")
	cl := testutils.NewTestClientBuilder().
		RecordErrorForObjectsMatchingLabels(testutils.ClientMethodList, client.ObjectKey{}, ctListGVK, nil, transientErr).
		Build()

	shortBackoff := wait.Backoff{Duration: time.Millisecond, Factor: 1.0, Steps: 3}
	err := SynchronizeTopologyWithRetry(ctx, cl, logr.Discard(), newKaiBackends(cl), shortBackoff)
	require.Error(t, err, "should fail after exhausting retry budget")
	assert.True(t, apierrors.IsServiceUnavailable(err))
}

// mockTimeoutError is a net.Error that reports Timeout() == true, mimicking a
// transport-level dial timeout (e.g. API server rolling restart, network partition).
type mockTimeoutError struct{}

func (mockTimeoutError) Error() string   { return "i/o timeout" }
func (mockTimeoutError) Timeout() bool   { return true }
func (mockTimeoutError) Temporary() bool { return true }

func TestIsTransientAPIError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantRetry bool
	}{
		{
			name:      "apierrors.InternalError is transient",
			err:       apierrors.NewInternalError(fmt.Errorf("internal")),
			wantRetry: true,
		},
		{
			name:      "apierrors.ServiceUnavailable is transient",
			err:       apierrors.NewServiceUnavailable("unavailable"),
			wantRetry: true,
		},
		{
			name:      "apierrors.Forbidden is permanent",
			err:       apierrors.NewForbidden(schema.GroupResource{}, "", fmt.Errorf("denied")),
			wantRetry: false,
		},
		{
			name:      "net.Error timeout is transient",
			err:       mockTimeoutError{},
			wantRetry: true,
		},
		{
			name:      "io.EOF is transient",
			err:       io.EOF,
			wantRetry: true,
		},
		{
			name:      "ECONNREFUSED is transient",
			err:       syscall.ECONNREFUSED,
			wantRetry: true,
		},
		{
			name:      "wrapped net.Error timeout is transient",
			err:       fmt.Errorf("wrapped: %w", mockTimeoutError{}),
			wantRetry: true,
		},
		{
			name:      "wrapped ECONNREFUSED is transient",
			err:       fmt.Errorf("wrapped: %w", syscall.ECONNREFUSED),
			wantRetry: true,
		},
		{
			name:      "net.Error non-timeout is not transient",
			err:       &net.OpError{Err: fmt.Errorf("some non-timeout net error")},
			wantRetry: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantRetry, isTransientAPIError(tc.err))
		})
	}
}

func TestSynchronizeTopologyWithRetry_TransportErrorRetried(t *testing.T) {
	ctx := context.Background()
	ct := createTestClusterTopology(topologyName, []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	})
	base := testutils.CreateDefaultFakeClient([]client.Object{ct})
	// Inject a transport-level timeout for the first 2 List calls; the 3rd succeeds.
	cl := &transientListClient{
		Client:    base,
		failCount: 2,
		failErr:   mockTimeoutError{},
	}

	err := SynchronizeTopologyWithRetry(ctx, cl, logr.Discard(), newKaiBackends(base), fastBackoff)
	require.NoError(t, err)
	assert.Equal(t, 3, cl.callCount, "expected 2 transient transport failures then 1 success")
}

func TestBuildSchedulerReferenceMap(t *testing.T) {
	refs := []grovecorev1alpha1.SchedulerTopologyBinding{
		{SchedulerName: "kai-scheduler", TopologyReference: "kai-topo"},
		{SchedulerName: "other-scheduler", TopologyReference: "other-topo"},
	}
	m := BuildSchedulerReferenceMap(refs)
	assert.NotNil(t, m["kai-scheduler"])
	assert.Equal(t, "kai-topo", m["kai-scheduler"].TopologyReference)
	assert.Nil(t, m["nonexistent"])
	assert.Empty(t, BuildSchedulerReferenceMap(nil))
}
