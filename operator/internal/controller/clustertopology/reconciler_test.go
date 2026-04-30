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

package clustertopology

import (
	"context"
	"fmt"
	"testing"

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	internalconstants "github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// -- Fake TopologyAwareSchedBackend for testing --

type fakeBackend struct {
	name         string
	syncErr      error
	driftInSync  bool
	driftMessage string
	driftGen     int64
	driftErr     error
	syncCalled   bool
	driftCalled  bool
}

func (f *fakeBackend) Name() string { return f.name }

func (f *fakeBackend) SyncTopology(_ context.Context, _ client.Client, _ *grovecorev1alpha1.ClusterTopology) error {
	f.syncCalled = true
	return f.syncErr
}

func (f *fakeBackend) OnTopologyDelete(_ context.Context, _ client.Client, _ *grovecorev1alpha1.ClusterTopology) error {
	return nil
}

func (f *fakeBackend) TopologyGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "fake", Version: "v1", Resource: "topologies"}
}

func (f *fakeBackend) TopologyResourceName(ct *grovecorev1alpha1.ClusterTopology) string {
	return ct.Name
}

func (f *fakeBackend) CheckTopologyDrift(_ context.Context, _ *grovecorev1alpha1.ClusterTopology, _ grovecorev1alpha1.SchedulerTopologyReference) (bool, string, int64, error) {
	f.driftCalled = true
	return f.driftInSync, f.driftMessage, f.driftGen, f.driftErr
}

// Backend interface stubs (not exercised by the CT controller).
func (f *fakeBackend) Init() error { return nil }

func (f *fakeBackend) SyncPodGang(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	return nil
}

func (f *fakeBackend) OnPodGangDelete(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	return nil
}

func (f *fakeBackend) PreparePod(_ *corev1.Pod) {}

func (f *fakeBackend) ValidatePodCliqueSet(_ context.Context, _ *grovecorev1alpha1.PodCliqueSet) error {
	return nil
}

// fakeNonTASBackend is a backend that does not implement TopologyAwareSchedBackend.
type fakeNonTASBackend struct {
	name string
}

func (f *fakeNonTASBackend) Name() string { return f.name }

func (f *fakeNonTASBackend) Init() error { return nil }

func (f *fakeNonTASBackend) SyncPodGang(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	return nil
}

func (f *fakeNonTASBackend) OnPodGangDelete(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	return nil
}

func (f *fakeNonTASBackend) PreparePod(_ *corev1.Pod) {}

func (f *fakeNonTASBackend) ValidatePodCliqueSet(_ context.Context, _ *grovecorev1alpha1.PodCliqueSet) error {
	return nil
}

// Verify interface compliance at compile time.
var _ scheduler.TopologyAwareSchedBackend = (*fakeBackend)(nil)

var _ scheduler.Backend = (*fakeNonTASBackend)(nil)

// -- Helpers --

// drainEvents reads all available events from the channel without blocking.
func drainEvents(ch <-chan string) []string {
	var events []string
	for {
		select {
		case e := <-ch:
			events = append(events, e)
		default:
			return events
		}
	}
}

func createTestCT(name string) *grovecorev1alpha1.ClusterTopology {
	return &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			UID:        uuid.NewUUID(),
			Generation: 1,
		},
		Spec: grovecorev1alpha1.ClusterTopologySpec{
			Levels: []grovecorev1alpha1.TopologyLevel{
				{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
				{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
			},
		},
	}
}

func doReconcile(r *Reconciler, name string) (ctrl.Result, error) {
	return r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{Name: name},
	})
}

func getDriftCondition(ct *grovecorev1alpha1.ClusterTopology) *metav1.Condition {
	return meta.FindStatusCondition(ct.Status.Conditions, apicommonconstants.ConditionSchedulerTopologyDrift)
}

// -- Tests --

func TestReconcile_AutoManaged_SyncsTopology(t *testing.T) {
	ct := createTestCT("my-topology")
	cl := testutils.NewTestClientBuilder().
		WithObjects(ct).
		WithStatusSubresource(ct).
		Build()

	backend := &fakeBackend{name: "kai-scheduler"}
	r := &Reconciler{
		Client:   cl,
		backends: map[string]scheduler.Backend{"kai-scheduler": backend},
		recorder: record.NewFakeRecorder(10),
	}

	result, err := doReconcile(r, "my-topology")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
	assert.True(t, backend.syncCalled, "SyncTopology should have been called")
	assert.False(t, backend.driftCalled, "CheckTopologyDrift should not have been called")

	// Verify status was updated
	fetched := &grovecorev1alpha1.ClusterTopology{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "my-topology"}, fetched))
	assert.Equal(t, int64(1), fetched.Status.ObservedGeneration)
	require.Len(t, fetched.Status.SchedulerTopologyStatuses, 1)
	assert.True(t, fetched.Status.SchedulerTopologyStatuses[0].InSync)
	assert.Equal(t, "kai-scheduler", fetched.Status.SchedulerTopologyStatuses[0].SchedulerName)
	assert.Equal(t, "my-topology", fetched.Status.SchedulerTopologyStatuses[0].TopologyReference)

	cond := getDriftCondition(fetched)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, apicommonconstants.ConditionReasonInSync, cond.Reason)
}

func TestReconcile_ExternallyManaged_InSync(t *testing.T) {
	ct := createTestCT("my-topology")
	ct.Spec.SchedulerTopologyReferences = []grovecorev1alpha1.SchedulerTopologyReference{
		{SchedulerName: "kai-scheduler", TopologyReference: "external-topology"},
	}
	cl := testutils.NewTestClientBuilder().
		WithObjects(ct).
		WithStatusSubresource(ct).
		Build()

	backend := &fakeBackend{
		name:        "kai-scheduler",
		driftInSync: true,
		driftGen:    5,
	}
	r := &Reconciler{
		Client:   cl,
		backends: map[string]scheduler.Backend{"kai-scheduler": backend},
		recorder: record.NewFakeRecorder(10),
	}

	result, err := doReconcile(r, "my-topology")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
	assert.False(t, backend.syncCalled, "SyncTopology should not have been called for externally-managed")
	assert.True(t, backend.driftCalled, "CheckTopologyDrift should have been called")

	fetched := &grovecorev1alpha1.ClusterTopology{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "my-topology"}, fetched))
	require.Len(t, fetched.Status.SchedulerTopologyStatuses, 1)
	status := fetched.Status.SchedulerTopologyStatuses[0]
	assert.True(t, status.InSync)
	assert.Equal(t, "external-topology", status.TopologyReference)
	assert.Equal(t, int64(5), status.SchedulerBackendTopologyObservedGeneration)

	cond := getDriftCondition(fetched)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
}

func TestReconcile_ExternallyManaged_Drift(t *testing.T) {
	ct := createTestCT("my-topology")
	ct.Spec.SchedulerTopologyReferences = []grovecorev1alpha1.SchedulerTopologyReference{
		{SchedulerName: "kai-scheduler", TopologyReference: "external-topology"},
	}
	cl := testutils.NewTestClientBuilder().
		WithObjects(ct).
		WithStatusSubresource(ct).
		Build()

	backend := &fakeBackend{
		name:         "kai-scheduler",
		driftInSync:  false,
		driftMessage: "KAI Topology levels differ from ClusterTopology levels",
		driftGen:     3,
	}
	r := &Reconciler{
		Client:   cl,
		backends: map[string]scheduler.Backend{"kai-scheduler": backend},
		recorder: record.NewFakeRecorder(10),
	}

	result, err := doReconcile(r, "my-topology")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	fetched := &grovecorev1alpha1.ClusterTopology{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "my-topology"}, fetched))
	require.Len(t, fetched.Status.SchedulerTopologyStatuses, 1)
	assert.False(t, fetched.Status.SchedulerTopologyStatuses[0].InSync)
	assert.Equal(t, "KAI Topology levels differ from ClusterTopology levels", fetched.Status.SchedulerTopologyStatuses[0].Message)

	cond := getDriftCondition(fetched)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, apicommonconstants.ConditionReasonDrift, cond.Reason)
}

func TestReconcile_SyncTopologyError(t *testing.T) {
	ct := createTestCT("my-topology")
	cl := testutils.NewTestClientBuilder().
		WithObjects(ct).
		WithStatusSubresource(ct).
		Build()

	backend := &fakeBackend{
		name:    "kai-scheduler",
		syncErr: fmt.Errorf("sync failed"),
	}
	r := &Reconciler{
		Client:   cl,
		backends: map[string]scheduler.Backend{"kai-scheduler": backend},
		recorder: record.NewFakeRecorder(10),
	}

	_, err := doReconcile(r, "my-topology")
	assert.Error(t, err, "reconcile should return the sync error")

	fetched := &grovecorev1alpha1.ClusterTopology{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "my-topology"}, fetched))
	require.Len(t, fetched.Status.SchedulerTopologyStatuses, 1)
	assert.False(t, fetched.Status.SchedulerTopologyStatuses[0].InSync)
	assert.Contains(t, fetched.Status.SchedulerTopologyStatuses[0].Message, "sync failed")
	assert.Equal(t, "my-topology", fetched.Status.SchedulerTopologyStatuses[0].TopologyReference)

	cond := getDriftCondition(fetched)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
}

func TestReconcile_CheckDriftError(t *testing.T) {
	ct := createTestCT("my-topology")
	ct.Spec.SchedulerTopologyReferences = []grovecorev1alpha1.SchedulerTopologyReference{
		{SchedulerName: "kai-scheduler", TopologyReference: "ext-topo"},
	}
	cl := testutils.NewTestClientBuilder().
		WithObjects(ct).
		WithStatusSubresource(ct).
		Build()

	backend := &fakeBackend{
		name:     "kai-scheduler",
		driftErr: fmt.Errorf("drift check failed"),
	}
	r := &Reconciler{
		Client:   cl,
		backends: map[string]scheduler.Backend{"kai-scheduler": backend},
		recorder: record.NewFakeRecorder(10),
	}

	_, err := doReconcile(r, "my-topology")
	assert.Error(t, err, "reconcile should return the drift check error")

	// Verify status reflects the error
	fetched := &grovecorev1alpha1.ClusterTopology{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "my-topology"}, fetched))
	require.Len(t, fetched.Status.SchedulerTopologyStatuses, 1)
	assert.False(t, fetched.Status.SchedulerTopologyStatuses[0].InSync)
	assert.Contains(t, fetched.Status.SchedulerTopologyStatuses[0].Message, "drift check failed")
}

func TestReconcile_NonTASBackendIgnored(t *testing.T) {
	ct := createTestCT("my-topology")
	cl := testutils.NewTestClientBuilder().
		WithObjects(ct).
		WithStatusSubresource(ct).
		Build()

	// Non-TAS backend — only implements Backend (not TopologyAwareSchedBackend)
	nonTAS := &fakeNonTASBackend{name: "kube-scheduler"}
	tasBackend := &fakeBackend{name: "kai-scheduler"}
	r := &Reconciler{
		Client: cl,
		backends: map[string]scheduler.Backend{
			"kube-scheduler": nonTAS,
			"kai-scheduler":  tasBackend,
		},
		recorder: record.NewFakeRecorder(10),
	}

	result, err := doReconcile(r, "my-topology")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Only the TAS-capable backend should have a status entry
	fetched := &grovecorev1alpha1.ClusterTopology{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "my-topology"}, fetched))
	require.Len(t, fetched.Status.SchedulerTopologyStatuses, 1)
	assert.Equal(t, "kai-scheduler", fetched.Status.SchedulerTopologyStatuses[0].SchedulerName)
}

func TestReconcile_ReferencedBackendDisabled_SetsTopologyNotFound(t *testing.T) {
	ct := createTestCT("my-topology")
	ct.Spec.SchedulerTopologyReferences = []grovecorev1alpha1.SchedulerTopologyReference{
		{SchedulerName: "kai-scheduler", TopologyReference: "external-topology"},
	}
	cl := testutils.NewTestClientBuilder().
		WithObjects(ct).
		WithStatusSubresource(ct).
		Build()

	r := &Reconciler{
		Client:   cl,
		backends: map[string]scheduler.Backend{},
		recorder: record.NewFakeRecorder(10),
	}

	result, err := doReconcile(r, "my-topology")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	fetched := &grovecorev1alpha1.ClusterTopology{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "my-topology"}, fetched))
	require.Len(t, fetched.Status.SchedulerTopologyStatuses, 1)
	status := fetched.Status.SchedulerTopologyStatuses[0]
	assert.False(t, status.InSync)
	assert.Equal(t, "kai-scheduler", status.SchedulerName)
	assert.Equal(t, "external-topology", status.TopologyReference)
	assert.Contains(t, status.Message, `scheduler backend "kai-scheduler" is not enabled`)

	cond := getDriftCondition(fetched)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionUnknown, cond.Status)
	assert.Equal(t, apicommonconstants.ConditionReasonTopologyNotFound, cond.Reason)
}

func TestReconcile_ReferencedBackendNotTopologyAware_SetsTopologyNotFound(t *testing.T) {
	ct := createTestCT("my-topology")
	ct.Spec.SchedulerTopologyReferences = []grovecorev1alpha1.SchedulerTopologyReference{
		{SchedulerName: "default-scheduler", TopologyReference: "external-topology"},
	}
	cl := testutils.NewTestClientBuilder().
		WithObjects(ct).
		WithStatusSubresource(ct).
		Build()

	r := &Reconciler{
		Client: cl,
		backends: map[string]scheduler.Backend{
			"default-scheduler": &fakeNonTASBackend{name: "default-scheduler"},
		},
		recorder: record.NewFakeRecorder(10),
	}

	result, err := doReconcile(r, "my-topology")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	fetched := &grovecorev1alpha1.ClusterTopology{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "my-topology"}, fetched))
	require.Len(t, fetched.Status.SchedulerTopologyStatuses, 1)
	status := fetched.Status.SchedulerTopologyStatuses[0]
	assert.False(t, status.InSync)
	assert.Equal(t, "default-scheduler", status.SchedulerName)
	assert.Equal(t, "external-topology", status.TopologyReference)
	assert.Contains(t, status.Message, `scheduler backend "default-scheduler" does not support topology management`)

	cond := getDriftCondition(fetched)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionUnknown, cond.Status)
	assert.Equal(t, apicommonconstants.ConditionReasonTopologyNotFound, cond.Reason)
}

func TestReconcile_NotFound(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	r := &Reconciler{
		Client:   cl,
		backends: map[string]scheduler.Backend{},
		recorder: record.NewFakeRecorder(10),
	}

	result, err := doReconcile(r, "deleted-topology")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestReconcile_NoTASBackends_RemovesCondition(t *testing.T) {
	ct := createTestCT("my-topology")
	cl := testutils.NewTestClientBuilder().
		WithObjects(ct).
		WithStatusSubresource(ct).
		Build()

	r := &Reconciler{
		Client:   cl,
		backends: map[string]scheduler.Backend{},
		recorder: record.NewFakeRecorder(10),
	}

	result, err := doReconcile(r, "my-topology")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	fetched := &grovecorev1alpha1.ClusterTopology{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "my-topology"}, fetched))
	assert.Empty(t, fetched.Status.SchedulerTopologyStatuses)
	cond := getDriftCondition(fetched)
	assert.Nil(t, cond, "condition should be removed when there are no TAS backends")
}

func TestReconcile_EmitsDriftEvents(t *testing.T) {
	ct := createTestCT("my-topology")
	ct.Spec.SchedulerTopologyReferences = []grovecorev1alpha1.SchedulerTopologyReference{
		{SchedulerName: "kai-scheduler", TopologyReference: "external-topology"},
	}
	cl := testutils.NewTestClientBuilder().
		WithObjects(ct).
		WithStatusSubresource(ct).
		Build()

	backend := &fakeBackend{name: "kai-scheduler"}
	fakeRecorder := record.NewFakeRecorder(10)
	r := &Reconciler{
		Client:   cl,
		backends: map[string]scheduler.Backend{"kai-scheduler": backend},
		recorder: fakeRecorder,
	}

	// First reconcile: Unknown -> InSync (transition occurs) -> Normal event
	backend.driftInSync = true
	_, err := doReconcile(r, "my-topology")
	require.NoError(t, err)

	var firstEvent string
	select {
	case event := <-fakeRecorder.Events:
		firstEvent = event
	default:
		t.Fatal("expected a sync event on first reconcile transition")
	}
	assert.Contains(t, firstEvent, "Normal")
	assert.Contains(t, firstEvent, internalconstants.ReasonTopologyInSync)

	// Second reconcile: InSync -> Drift (transition occurs) -> Warning event
	backend.driftInSync = false
	backend.driftMessage = "KAI Topology levels differ"
	_, err = doReconcile(r, "my-topology")
	require.NoError(t, err)

	driftEvent := drainEvents(fakeRecorder.Events)
	require.Len(t, driftEvent, 1, "expected exactly one Warning event for drift transition")
	assert.Contains(t, driftEvent[0], "Warning")
	assert.Contains(t, driftEvent[0], internalconstants.ReasonTopologyDriftDetected)
	assert.Contains(t, driftEvent[0], "drifted")

	// Third reconcile: Drift -> InSync (transition occurs) -> Normal event
	backend.driftInSync = true
	backend.driftMessage = ""
	_, err = doReconcile(r, "my-topology")
	require.NoError(t, err)

	syncEvent := drainEvents(fakeRecorder.Events)
	require.Len(t, syncEvent, 1, "expected exactly one Normal event for sync transition")
	assert.Contains(t, syncEvent[0], "Normal")
	assert.Contains(t, syncEvent[0], internalconstants.ReasonTopologyInSync)
}

func TestReconcile_DoesNotEmitEventWhenDriftStatusIsStable(t *testing.T) {
	ct := createTestCT("my-topology")
	ct.Spec.SchedulerTopologyReferences = []grovecorev1alpha1.SchedulerTopologyReference{
		{SchedulerName: "kai-scheduler", TopologyReference: "external-topology"},
	}
	cl := testutils.NewTestClientBuilder().
		WithObjects(ct).
		WithStatusSubresource(ct).
		Build()

	backend := &fakeBackend{name: "kai-scheduler", driftInSync: true}
	fakeRecorder := record.NewFakeRecorder(10)
	r := &Reconciler{
		Client:   cl,
		backends: map[string]scheduler.Backend{"kai-scheduler": backend},
		recorder: fakeRecorder,
	}

	_, err := doReconcile(r, "my-topology")
	require.NoError(t, err)
	require.Len(t, drainEvents(fakeRecorder.Events), 1, "expected an event for the initial Unknown -> InSync transition")

	_, err = doReconcile(r, "my-topology")
	require.NoError(t, err)
	assert.Empty(t, drainEvents(fakeRecorder.Events), "expected no event when the drift status remains InSync")
}

func TestReconcile_MixedBackendOutcomes(t *testing.T) {
	ct := createTestCT("my-topology")
	ct.Spec.SchedulerTopologyReferences = []grovecorev1alpha1.SchedulerTopologyReference{
		{SchedulerName: "beta-scheduler", TopologyReference: "external-beta-topology"},
	}
	cl := testutils.NewTestClientBuilder().
		WithObjects(ct).
		WithStatusSubresource(ct).
		Build()

	alphaBackend := &fakeBackend{name: "alpha-scheduler"}
	betaBackend := &fakeBackend{
		name:     "beta-scheduler",
		driftErr: fmt.Errorf("drift check failed"),
	}
	r := &Reconciler{
		Client: cl,
		backends: map[string]scheduler.Backend{
			"alpha-scheduler": alphaBackend,
			"beta-scheduler":  betaBackend,
		},
		recorder: record.NewFakeRecorder(10),
	}

	_, err := doReconcile(r, "my-topology")
	require.Error(t, err)
	assert.True(t, alphaBackend.syncCalled)
	assert.True(t, betaBackend.driftCalled)

	fetched := &grovecorev1alpha1.ClusterTopology{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "my-topology"}, fetched))
	require.Len(t, fetched.Status.SchedulerTopologyStatuses, 2)

	statusByScheduler := make(map[string]grovecorev1alpha1.SchedulerTopologyStatus, len(fetched.Status.SchedulerTopologyStatuses))
	for _, status := range fetched.Status.SchedulerTopologyStatuses {
		statusByScheduler[status.SchedulerName] = status
	}

	alphaStatus, ok := statusByScheduler["alpha-scheduler"]
	require.True(t, ok)
	assert.True(t, alphaStatus.InSync)
	assert.Equal(t, "my-topology", alphaStatus.TopologyReference)

	betaStatus, ok := statusByScheduler["beta-scheduler"]
	require.True(t, ok)
	assert.False(t, betaStatus.InSync)
	assert.Equal(t, "external-beta-topology", betaStatus.TopologyReference)
	assert.Contains(t, betaStatus.Message, "drift check failed")

	cond := getDriftCondition(fetched)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, apicommonconstants.ConditionReasonDrift, cond.Reason)
}

// -- Unit tests for helper functions --

func TestSetSchedulerTopologyDriftCondition_AllInSync(t *testing.T) {
	ct := createTestCT("test")
	statuses := []grovecorev1alpha1.SchedulerTopologyStatus{
		{SchedulerTopologyReference: grovecorev1alpha1.SchedulerTopologyReference{SchedulerName: "a"}, InSync: true},
		{SchedulerTopologyReference: grovecorev1alpha1.SchedulerTopologyReference{SchedulerName: "b"}, InSync: true},
	}
	setSchedulerTopologyDriftCondition(ct, statuses, false)
	cond := getDriftCondition(ct)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, apicommonconstants.ConditionReasonInSync, cond.Reason)
}

func TestSetSchedulerTopologyDriftCondition_SomeDrift(t *testing.T) {
	ct := createTestCT("test")
	statuses := []grovecorev1alpha1.SchedulerTopologyStatus{
		{SchedulerTopologyReference: grovecorev1alpha1.SchedulerTopologyReference{SchedulerName: "a"}, InSync: true},
		{SchedulerTopologyReference: grovecorev1alpha1.SchedulerTopologyReference{SchedulerName: "b"}, InSync: false},
	}
	setSchedulerTopologyDriftCondition(ct, statuses, false)
	cond := getDriftCondition(ct)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, apicommonconstants.ConditionReasonDrift, cond.Reason)
}

func TestSetSchedulerTopologyDriftCondition_Empty(t *testing.T) {
	ct := createTestCT("test")
	// Pre-set a condition so we can verify it gets removed
	meta.SetStatusCondition(&ct.Status.Conditions, metav1.Condition{
		Type:   apicommonconstants.ConditionSchedulerTopologyDrift,
		Status: metav1.ConditionTrue,
		Reason: apicommonconstants.ConditionReasonDrift,
	})
	setSchedulerTopologyDriftCondition(ct, nil, false)
	cond := getDriftCondition(ct)
	assert.Nil(t, cond, "condition should be removed when no statuses exist")
}

func TestSetSchedulerTopologyDriftCondition_TopologyNotFound(t *testing.T) {
	ct := createTestCT("test")
	statuses := []grovecorev1alpha1.SchedulerTopologyStatus{
		{SchedulerTopologyReference: grovecorev1alpha1.SchedulerTopologyReference{SchedulerName: "kai-scheduler"}, InSync: false},
	}
	setSchedulerTopologyDriftCondition(ct, statuses, true)
	cond := getDriftCondition(ct)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionUnknown, cond.Status)
	assert.Equal(t, apicommonconstants.ConditionReasonTopologyNotFound, cond.Reason)
}

// -- Unit tests for mapBackendTopologyToCT --

func TestMapBackendTopologyToCT_AutoManaged(t *testing.T) {
	ct := createTestCT("my-topology")
	cl := testutils.NewTestClientBuilder().WithObjects(ct).Build()
	r := &Reconciler{Client: cl}

	// Simulate a backend topology object with an OwnerReference to the CT.
	backendObj := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-topology",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: grovecorev1alpha1.SchemeGroupVersion.String(),
				Kind:       "ClusterTopology",
				Name:       "my-topology",
				UID:        ct.UID,
				Controller: ptr.To(true),
			}},
		},
	}

	mapFn := r.mapBackendTopologyToCT("kai-scheduler")
	requests := mapFn(context.Background(), backendObj)
	require.Len(t, requests, 1)
	assert.Equal(t, "my-topology", requests[0].Name)
}

func TestMapBackendTopologyToCT_ExternallyManaged(t *testing.T) {
	ct := createTestCT("my-topology")
	ct.Spec.SchedulerTopologyReferences = []grovecorev1alpha1.SchedulerTopologyReference{
		{SchedulerName: "kai-scheduler", TopologyReference: "external-kai-topo"},
	}
	cl := testutils.NewTestClientBuilder().WithObjects(ct).Build()
	r := &Reconciler{Client: cl}

	// Simulate a backend topology object with no CT OwnerReference.
	backendObj := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "external-kai-topo"},
	}

	mapFn := r.mapBackendTopologyToCT("kai-scheduler")
	requests := mapFn(context.Background(), backendObj)
	require.Len(t, requests, 1)
	assert.Equal(t, "my-topology", requests[0].Name)
}

func TestMapBackendTopologyToCT_UnrelatedTopology(t *testing.T) {
	ct := createTestCT("my-topology")
	cl := testutils.NewTestClientBuilder().WithObjects(ct).Build()
	r := &Reconciler{Client: cl}

	// Object with no OwnerReference and no matching schedulerTopologyReference.
	backendObj := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "unrelated-topology"},
	}

	mapFn := r.mapBackendTopologyToCT("kai-scheduler")
	requests := mapFn(context.Background(), backendObj)
	assert.Empty(t, requests)
}

func TestMapBackendTopologyToCT_WrongOwnerKind(t *testing.T) {
	ct := createTestCT("my-topology")
	ct.Spec.SchedulerTopologyReferences = []grovecorev1alpha1.SchedulerTopologyReference{
		{SchedulerName: "kai-scheduler", TopologyReference: "some-topology"},
	}
	cl := testutils.NewTestClientBuilder().WithObjects(ct).Build()
	r := &Reconciler{Client: cl}

	// Object with an OwnerReference that is NOT a ClusterTopology — should fall
	// through to the schedulerTopologyReferences path.
	backendObj := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "some-topology",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "some-deployment",
			}},
		},
	}

	mapFn := r.mapBackendTopologyToCT("kai-scheduler")
	requests := mapFn(context.Background(), backendObj)
	require.Len(t, requests, 1)
	assert.Equal(t, "my-topology", requests[0].Name)
}
