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

package clustertopology

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"
	"time"

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/kai"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"
	schedulertest "github.com/ai-dynamo/grove/operator/test/utils/scheduler"

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

func TestSynchronizeTopologyListsAndSyncs(t *testing.T) {
	ctx := context.Background()
	topologyLevels := []grovecorev1beta1.TopologyLevel{
		{Domain: grovecorev1beta1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1beta1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	ct := createTestClusterTopology(topologyName, topologyLevels)

	cl := schedulertest.NewKAIClient(t, ct)
	logger := logr.Discard()

	err := SynchronizeTopology(ctx, cl, logger, newKAIBackendMap(t, cl))
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
	ct1 := createTestClusterTopology("topology-a", []grovecorev1beta1.TopologyLevel{
		{Domain: grovecorev1beta1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
	})
	ct2 := createTestClusterTopology("topology-b", []grovecorev1beta1.TopologyLevel{
		{Domain: grovecorev1beta1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	})

	cl := schedulertest.NewKAIClient(t, ct1, ct2)
	logger := logr.Discard()

	err := SynchronizeTopology(ctx, cl, logger, newKAIBackendMap(t, cl))
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
	cl := schedulertest.NewKAIClient(t)
	logger := logr.Discard()

	err := SynchronizeTopology(ctx, cl, logger, newKAIBackendMap(t, cl))
	require.NoError(t, err)
}

func TestSynchronizeTopologyNoTASBackends(t *testing.T) {
	ctx := context.Background()
	ct := createTestClusterTopology(topologyName, []grovecorev1beta1.TopologyLevel{
		{Domain: grovecorev1beta1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	})
	cl := schedulertest.NewKAIClient(t, ct)
	logger := logr.Discard()

	// Pass nil backends
	err := SynchronizeTopology(ctx, cl, logger, nil)
	require.NoError(t, err)
}

func TestSynchronizeTopologySkipsExternallyManaged(t *testing.T) {
	ctx := context.Background()
	topologyLevels := []grovecorev1beta1.TopologyLevel{
		{Domain: grovecorev1beta1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1beta1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	ct := createTestClusterTopology(topologyName, topologyLevels)
	ct.Spec.SchedulerTopologyBindings = []grovecorev1beta1.SchedulerTopologyBinding{
		{SchedulerName: "kai-scheduler", TopologyReference: "external-kai-topology"},
	}

	cl := schedulertest.NewKAIClient(t, ct)
	logger := logr.Discard()

	// KAI backend is listed in schedulerTopologyReferences — SyncTopology should NOT be called.
	// If it were called, it would try to create a KAI Topology and succeed, so we verify
	// that no KAI Topology was created.
	err := SynchronizeTopology(ctx, cl, logger, newKAIBackendMap(t, cl))
	require.NoError(t, err)

	kaiTopology := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(ctx, client.ObjectKey{Name: topologyName}, kaiTopology)
	assert.True(t, apierrors.IsNotFound(err), "KAI Topology should not have been created for externally-managed CT")
}

func TestSynchronizeTopologyListError(t *testing.T) {
	ctx := context.Background()
	listErr := apierrors.NewInternalError(assert.AnError)
	ctListGVK := grovecorev1beta1.SchemeGroupVersion.WithKind("ClusterTopologyBindingList")
	cl := testutils.NewTestClientBuilder().
		WithScheme(schedulertest.NewKAIScheme(t)).
		RecordErrorForObjectsMatchingLabels(testutils.ClientMethodList, client.ObjectKey{}, ctListGVK, nil, listErr).
		Build()
	logger := logr.Discard()

	err := SynchronizeTopology(ctx, cl, logger, newKAIBackendMap(t, cl))
	assert.Error(t, err)
}

// TestSynchronizeTopologyRetriedAfterTransientListErrors verifies that an API status error and a
// connection error returned by the list call are both retried through the error wrapping that
// SynchronizeTopology adds, and that the synchronization completes once the list call succeeds.
func TestSynchronizeTopologyRetriedAfterTransientListErrors(t *testing.T) {
	ctx := context.Background()
	ct := createTestClusterTopology(topologyName, []grovecorev1beta1.TopologyLevel{
		{Domain: grovecorev1beta1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	})
	connectionRefused := &url.Error{Op: "Get", URL: "https://10.96.0.1:443/apis", Err: &net.OpError{
		Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED),
	}}
	cl := &flakyClient{
		Client:   schedulertest.NewKAIClient(t, ct),
		listErrs: []error{apierrors.NewServiceUnavailable("unavailable"), connectionRefused},
	}
	backends := newKAIBackendMap(t, cl)

	var retries []int
	err := k8sutils.RetryOnError(ctx, testRetryBackoff, k8sutils.IsRetriableError,
		func(attempt int, _ error) { retries = append(retries, attempt) },
		func(ctx context.Context) error {
			return SynchronizeTopology(ctx, cl, logr.Discard(), backends)
		})

	require.NoError(t, err)
	assert.Equal(t, 3, cl.listCalls)
	assert.Equal(t, []int{1, 2}, retries)
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: topologyName}, &kaitopologyv1alpha1.Topology{}))
}

// TestSynchronizeTopologyRetriedAfterPartialSync verifies that repeating the whole synchronization
// after it failed midway is safe: the topology that was already created is left alone and the one
// that failed is created.
func TestSynchronizeTopologyRetriedAfterPartialSync(t *testing.T) {
	ctx := context.Background()
	ct1 := createTestClusterTopology("topology-a", []grovecorev1beta1.TopologyLevel{
		{Domain: grovecorev1beta1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
	})
	ct2 := createTestClusterTopology("topology-b", []grovecorev1beta1.TopologyLevel{
		{Domain: grovecorev1beta1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	})
	cl := &flakyClient{
		Client:     schedulertest.NewKAIClient(t, ct1, ct2),
		createErrs: map[string][]error{"topology-b": {apierrors.NewInternalError(assert.AnError)}},
	}
	backends := newKAIBackendMap(t, cl)

	err := k8sutils.RetryOnError(ctx, testRetryBackoff, k8sutils.IsRetriableError, nil,
		func(ctx context.Context) error {
			return SynchronizeTopology(ctx, cl, logr.Discard(), backends)
		})

	require.NoError(t, err)
	assert.Equal(t, 2, cl.listCalls)
	assert.Equal(t, map[string]int{"topology-a": 1, "topology-b": 2}, cl.createCalls)
	for _, ct := range []*grovecorev1beta1.ClusterTopologyBinding{ct1, ct2} {
		kaiTopology := &kaitopologyv1alpha1.Topology{}
		require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: ct.Name}, kaiTopology))
		assert.True(t, metav1.IsControlledBy(kaiTopology, ct))
	}
}

// TestSynchronizeTopologyNotRetriedOnPermanentListError verifies that a permanent error is
// returned after a single attempt.
func TestSynchronizeTopologyNotRetriedOnPermanentListError(t *testing.T) {
	ctx := context.Background()
	forbidden := apierrors.NewForbidden(
		schema.GroupResource{Group: apicommonconstants.OperatorGroupName, Resource: "clustertopologybindings"}, "", assert.AnError)
	cl := &flakyClient{
		Client:   schedulertest.NewKAIClient(t),
		listErrs: []error{forbidden, forbidden, forbidden},
	}
	backends := newKAIBackendMap(t, cl)

	err := k8sutils.RetryOnError(ctx, testRetryBackoff, k8sutils.IsRetriableError,
		func(int, error) { t.Error("a permanent error must not be retried") },
		func(ctx context.Context) error {
			return SynchronizeTopology(ctx, cl, logr.Discard(), backends)
		})

	require.Error(t, err)
	assert.True(t, apierrors.IsForbidden(err))
	assert.Equal(t, 1, cl.listCalls)
}

func TestGetClusterTopologyLevels(t *testing.T) {
	tests := []struct {
		name              string
		clusterTopology   *grovecorev1beta1.ClusterTopologyBinding
		topologyName      string
		getError          *apierrors.StatusError
		expectedLevels    []grovecorev1beta1.TopologyLevel
		expectError       bool
		expectedErrorType error
	}{
		{
			name: "successfully retrieve topology levels",
			clusterTopology: &grovecorev1beta1.ClusterTopologyBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-topology",
				},
				Spec: grovecorev1beta1.ClusterTopologyBindingSpec{
					Levels: []grovecorev1beta1.TopologyLevel{
						{
							Domain: grovecorev1beta1.TopologyDomainRegion,
							Key:    "topology.kubernetes.io/region",
						},
						{
							Domain: grovecorev1beta1.TopologyDomainZone,
							Key:    "topology.kubernetes.io/zone",
						},
						{
							Domain: grovecorev1beta1.TopologyDomainHost,
							Key:    "kubernetes.io/hostname",
						},
					},
				},
			},
			topologyName: "test-topology",
			expectedLevels: []grovecorev1beta1.TopologyLevel{
				{
					Domain: grovecorev1beta1.TopologyDomainRegion,
					Key:    "topology.kubernetes.io/region",
				},
				{
					Domain: grovecorev1beta1.TopologyDomainZone,
					Key:    "topology.kubernetes.io/zone",
				},
				{
					Domain: grovecorev1beta1.TopologyDomainHost,
					Key:    "kubernetes.io/hostname",
				},
			},
			expectError: false,
		},
		{
			name: "topology not found",
			clusterTopology: &grovecorev1beta1.ClusterTopologyBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "existing-topology",
				},
				Spec: grovecorev1beta1.ClusterTopologyBindingSpec{
					Levels: []grovecorev1beta1.TopologyLevel{
						{
							Domain: grovecorev1beta1.TopologyDomainRegion,
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
			clusterTopology: &grovecorev1beta1.ClusterTopologyBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-topology",
				},
				Spec: grovecorev1beta1.ClusterTopologyBindingSpec{
					Levels: []grovecorev1beta1.TopologyLevel{
						{
							Domain: grovecorev1beta1.TopologyDomainZone,
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

func TestBuildSchedulerReferenceMap(t *testing.T) {
	refs := []grovecorev1beta1.SchedulerTopologyBinding{
		{SchedulerName: "kai-scheduler", TopologyReference: "kai-topo"},
		{SchedulerName: "other-scheduler", TopologyReference: "other-topo"},
	}
	m := BuildSchedulerReferenceMap(refs)
	assert.NotNil(t, m["kai-scheduler"])
	assert.Equal(t, "kai-topo", m["kai-scheduler"].TopologyReference)
	assert.Nil(t, m["nonexistent"])
	assert.Empty(t, BuildSchedulerReferenceMap(nil))
}

var testRetryBackoff = wait.Backoff{Duration: time.Millisecond, Factor: 1.0, Steps: 5}

// flakyClient returns the queued errors, in order, from List and from Create of the named object
// before delegating to the wrapped client. It also counts the calls.
type flakyClient struct {
	client.Client
	listErrs    []error
	createErrs  map[string][]error
	listCalls   int
	createCalls map[string]int
}

func (c *flakyClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	c.listCalls++
	if len(c.listErrs) > 0 {
		err := c.listErrs[0]
		c.listErrs = c.listErrs[1:]
		return err
	}
	return c.Client.List(ctx, list, opts...)
}

func (c *flakyClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if c.createCalls == nil {
		c.createCalls = map[string]int{}
	}
	name := obj.GetName()
	c.createCalls[name]++
	if errs := c.createErrs[name]; len(errs) > 0 {
		c.createErrs[name] = errs[1:]
		return fmt.Errorf("create %s: %w", name, errs[0])
	}
	return c.Client.Create(ctx, obj, opts...)
}

func newKAIBackendMap(t *testing.T, cl client.Client) map[string]scheduler.TopologyAwareBackend {
	t.Helper()
	profile := configv1beta1.SchedulerProfile{Name: configv1beta1.SchedulerNameKai}
	b := kai.New(cl, cl.Scheme(), nil, profile)
	require.NoError(t, b.Init(cl))
	return map[string]scheduler.TopologyAwareBackend{b.Name(): b.(scheduler.TopologyAwareBackend)}
}

// createTestClusterTopology creates a ClusterTopologyBinding with the given name and topology levels.
func createTestClusterTopology(name string, levels []grovecorev1beta1.TopologyLevel) *grovecorev1beta1.ClusterTopologyBinding {
	return &grovecorev1beta1.ClusterTopologyBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			UID:  uuid.NewUUID(),
		},
		Spec: grovecorev1beta1.ClusterTopologyBindingSpec{
			Levels: levels,
		},
	}
}
