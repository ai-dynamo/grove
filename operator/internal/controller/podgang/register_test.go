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

package podgang

import (
	"context"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/registry"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"
	schedulertest "github.com/ai-dynamo/grove/operator/test/utils/scheduler"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllerconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	workloadReconcileTimeout = 30 * time.Second
	workloadPollInterval     = 100 * time.Millisecond
)

// predicateTestCase describes a scenario and expected predicate result per event type.
type predicateTestCase struct {
	name                    string
	managedOld              bool
	managedNew              bool
	generationChanged       bool
	shouldAllowCreateEvent  bool
	shouldAllowDeleteEvent  bool
	shouldAllowGenericEvent bool
	shouldAllowUpdateEvent  bool
}

func TestPodGangSpecChangePredicate(t *testing.T) {
	pred := podGangSpecChangePredicate()

	tests := []predicateTestCase{
		{
			name:                    "managed PodGang create",
			managedOld:              true,
			managedNew:              true,
			shouldAllowCreateEvent:  true,
			shouldAllowDeleteEvent:  true,
			shouldAllowGenericEvent: false,
			shouldAllowUpdateEvent:  false,
		},
		{
			name:                    "unmanaged PodGang create",
			managedOld:              false,
			managedNew:              false,
			shouldAllowCreateEvent:  false,
			shouldAllowDeleteEvent:  false,
			shouldAllowGenericEvent: false,
			shouldAllowUpdateEvent:  false,
		},
		{
			name:                    "managed PodGang update with spec change (generation changed)",
			managedOld:              true,
			managedNew:              true,
			generationChanged:       true,
			shouldAllowCreateEvent:  true,
			shouldAllowDeleteEvent:  true,
			shouldAllowGenericEvent: false,
			shouldAllowUpdateEvent:  true,
		},
		{
			name:                    "managed PodGang update with status-only change (generation unchanged)",
			managedOld:              true,
			managedNew:              true,
			generationChanged:       false,
			shouldAllowCreateEvent:  true,
			shouldAllowDeleteEvent:  true,
			shouldAllowGenericEvent: false,
			shouldAllowUpdateEvent:  false,
		},
		{
			name:                    "update with old managed and new unmanaged",
			managedOld:              true,
			managedNew:              false,
			generationChanged:       true,
			shouldAllowCreateEvent:  false, // Create/Delete use newPG which is unmanaged
			shouldAllowDeleteEvent:  false,
			shouldAllowGenericEvent: false,
			shouldAllowUpdateEvent:  false,
		},
		{
			name:                    "update with old unmanaged and new managed",
			managedOld:              false,
			managedNew:              true,
			generationChanged:       true,
			shouldAllowCreateEvent:  true, // Create/Delete use newPG which is managed
			shouldAllowDeleteEvent:  true,
			shouldAllowGenericEvent: false,
			shouldAllowUpdateEvent:  false, // old is unmanaged
		},
		{
			name:                    "generic event always rejected",
			managedOld:              true,
			managedNew:              true,
			shouldAllowCreateEvent:  true,
			shouldAllowDeleteEvent:  true,
			shouldAllowGenericEvent: false,
			shouldAllowUpdateEvent:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldPG := testutils.NewPodGangBuilder("test-pg", "default").
				WithGeneration(1).
				WithManaged(tc.managedOld).
				WithPodGroup("pg0", 1).
				Build()
			newPG := testutils.NewPodGangBuilder("test-pg", "default").
				WithGeneration(1).
				WithManaged(tc.managedNew).
				WithPodGroup("pg0", 1).
				Build()
			if tc.generationChanged {
				newPG.SetGeneration(oldPG.GetGeneration() + 1)
			}

			assert.Equal(t, tc.shouldAllowCreateEvent, pred.Create(event.CreateEvent{Object: newPG}), "Create")
			assert.Equal(t, tc.shouldAllowDeleteEvent, pred.Delete(event.DeleteEvent{Object: newPG}), "Delete")
			assert.Equal(t, tc.shouldAllowGenericEvent, pred.Generic(event.GenericEvent{Object: newPG}), "Generic")
			assert.Equal(t, tc.shouldAllowUpdateEvent, pred.Update(event.UpdateEvent{ObjectOld: oldPG, ObjectNew: newPG}), "Update")
		})
	}
}

func TestPodGangFirstScheduledPredicate(t *testing.T) {
	pred := podGangFirstScheduledPredicate()
	unscheduled := testutils.NewPodGangBuilder("test-pg", "default").WithManaged(true).Build()
	scheduled := testutils.NewPodGangBuilder("test-pg", "default").WithManaged(true).WithLastScheduled().Build()
	unmanagedUnscheduled := testutils.NewPodGangBuilder("test-pg", "default").Build()
	unmanagedScheduled := testutils.NewPodGangBuilder("test-pg", "default").WithLastScheduled().Build()

	assert.True(t, pred.Update(event.UpdateEvent{ObjectOld: unscheduled, ObjectNew: scheduled}), "first scheduled")
	assert.False(t, pred.Update(event.UpdateEvent{ObjectOld: scheduled, ObjectNew: scheduled}), "already scheduled")
	assert.False(t, pred.Update(event.UpdateEvent{ObjectOld: unscheduled, ObjectNew: unscheduled}), "still unscheduled")
	assert.False(t, pred.Update(event.UpdateEvent{ObjectOld: unmanagedUnscheduled, ObjectNew: unmanagedScheduled}), "unmanaged")
	assert.False(t, pred.Create(event.CreateEvent{Object: scheduled}), "Create")
	assert.False(t, pred.Delete(event.DeleteEvent{Object: scheduled}), "Delete")
	assert.False(t, pred.Generic(event.GenericEvent{Object: scheduled}), "Generic")
}

func TestMapPodGangToDependentPodGangs(t *testing.T) {
	const (
		pcsName     = "demo"
		namespace   = "default"
		anchorEpoch = "1000"
		tailEpoch   = "1001"
	)
	pgm := testutils.NewPodGangMapBuilder(pcsName, namespace, "pcs-uid", 0).
		WithEntries(
			testutils.NewAnchorEntry("hash", anchorEpoch, "sg", 0),
			testutils.NewPodGangEntryBuilder("hash", tailEpoch).
				WithRole(grovecorev1alpha1.PodGangEntryRoleTail).
				WithPCSGReplicaIndices(map[string][]int32{"sg": {1, 2}}).
				WithDependsOn(anchorEpoch).
				Build(),
		).
		Build()
	cl := testutils.NewTestClientBuilder().WithObjects(pgm).Build()
	mapFn := mapPodGangToDependentPodGangs(cl)
	podGangAtEpoch := func(epoch string) *groveschedulerv1alpha1.PodGang {
		return testutils.NewPodGangBuilder("pg", namespace).WithLabels(map[string]string{
			apicommon.LabelPartOfKey:                pcsName,
			apicommon.LabelPodCliqueSetReplicaIndex: "0",
			apicommon.LabelEpoch:                    epoch,
		}).Build()
	}

	got := mapFn(context.Background(), podGangAtEpoch(anchorEpoch))
	assert.ElementsMatch(t, []reconcile.Request{
		{NamespacedName: types.NamespacedName{Namespace: namespace, Name: "demo-0-1001-sg-1"}},
		{NamespacedName: types.NamespacedName{Namespace: namespace, Name: "demo-0-1001-sg-2"}},
	}, got, "the anchor maps to the tail PodGangs that depend on it")
	assert.Empty(t, mapFn(context.Background(), podGangAtEpoch(tailEpoch)), "nothing depends on the tail")
	assert.Empty(t, mapFn(context.Background(), testutils.NewPodGangBuilder("legacy", namespace).Build()), "a PodGang without an epoch has no dependents")
}

func TestPodGangRecreatesDeletedWorkloadResources(t *testing.T) {
	config := schedulertest.StartWorkloadEnvtest(t,
		"GenericWorkload=true,CompositePodGroup=true,TopologyAwareWorkloadScheduling=true",
		"../../../../scheduler/api/core/v1alpha1/crds")
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, groveschedulerv1alpha1.AddToScheme(scheme))
	directClient, err := client.New(config, client.Options{Scheme: scheme})
	require.NoError(t, err)
	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		// Controller names remain process-global between go test -count runs.
		Controller: controllerconfig.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)
	schedulers, err := registry.New(mgr.GetClient(), directClient, scheme, nil, configv1alpha1.SchedulerConfiguration{
		DefaultProfileName: string(configv1alpha1.SchedulerNameKube),
		Profiles: []configv1alpha1.SchedulerProfile{{
			Name:   configv1alpha1.SchedulerNameKube,
			Config: &runtime.RawExtension{Raw: []byte(`{"gangScheduling":true}`)},
		}},
	})
	require.NoError(t, err)
	reconciler := NewReconciler(mgr, configv1alpha1.PodGangControllerConfiguration{ConcurrentSyncs: ptr.To(1)}, schedulers)
	require.NoError(t, reconciler.RegisterWithManager(mgr))
	ctx, cancel := context.WithCancel(t.Context())
	managerDone := make(chan error, 1)
	go func() { managerDone <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-managerDone:
			require.NoError(t, err)
		case <-time.After(workloadReconcileTimeout):
			t.Error("manager did not stop")
		}
	})
	syncCtx, syncCancel := context.WithTimeout(ctx, workloadReconcileTimeout)
	defer syncCancel()
	require.True(t, mgr.GetCache().WaitForCacheSync(syncCtx))
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "was-owner-watch"}}
	require.NoError(t, directClient.Create(ctx, ns))
	gang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name: "was-gang", Namespace: ns.Name,
			Labels: map[string]string{apicommon.LabelManagedByKey: apicommon.LabelManagedByValue},
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{{
				Name: "was-leaf", MinReplicas: 2, PodReferences: []groveschedulerv1alpha1.NamespacedName{},
			}},
			TopologyConstraintGroupConfigs: []groveschedulerv1alpha1.TopologyConstraintGroupConfig{{
				Name: "rack", PodGroupNames: []string{"was-leaf"},
			}},
		},
	}
	require.NoError(t, directClient.Create(ctx, gang))
	resources := []client.Object{
		&schedulingv1beta1.Workload{ObjectMeta: metav1.ObjectMeta{Name: gang.Name, Namespace: ns.Name}},
		&schedulingv1alpha3.CompositePodGroup{ObjectMeta: metav1.ObjectMeta{Name: gang.Name, Namespace: ns.Name}},
	}
	leaves := &schedulingv1beta1.PodGroupList{}
	composites := &schedulingv1alpha3.CompositePodGroupList{}
	require.Eventually(t, func() bool {
		return directClient.List(ctx, leaves, client.InNamespace(ns.Name)) == nil &&
			directClient.List(ctx, composites, client.InNamespace(ns.Name)) == nil &&
			len(leaves.Items) == 1 && len(composites.Items) == 2
	}, workloadReconcileTimeout, workloadPollInterval)
	leafName := leaves.Items[0].Name
	require.NotEqual(t, "was-leaf", leafName, "runtime identity must include the gang")
	var childName string
	for i := range composites.Items {
		if composites.Items[i].Name != gang.Name {
			childName = composites.Items[i].Name
			resources = append(resources, &composites.Items[i])
		}
	}
	resources = append(resources, &leaves.Items[0])
	for _, resource := range resources {
		require.Eventually(t, func() bool {
			return directClient.Get(ctx, client.ObjectKeyFromObject(resource), resource) == nil
		}, workloadReconcileTimeout, workloadPollInterval)
		require.True(t, metav1.IsControlledBy(resource, gang))
	}

	// Releasing the Grove minimum must not prevent recovery of any one missing
	// generated object, and recovery must be triggered without a PodGang edit.
	require.NoError(t, directClient.Get(ctx, client.ObjectKeyFromObject(gang), gang))
	gang.Spec.PodGroups[0].MinReplicas = 0
	require.NoError(t, directClient.Update(ctx, gang))
	generation := gang.Generation
	for _, resource := range resources {
		gvk, err := directClient.GroupVersionKindFor(resource)
		require.NoError(t, err)
		t.Run(gvk.Kind+"/"+resource.GetName(), func(t *testing.T) {
			require.NoError(t, directClient.Get(ctx, client.ObjectKeyFromObject(resource), resource))
			uid := resource.GetUID()
			require.NoError(t, directClient.Delete(ctx, resource))
			if _, ok := resource.(*schedulingv1beta1.PodGroup); ok {
				require.NoError(t, directClient.Get(ctx, client.ObjectKeyFromObject(resource), resource))
				require.False(t, resource.GetDeletionTimestamp().IsZero())
				require.Equal(t, []string{"scheduling.k8s.io/podgroup-protection"}, resource.GetFinalizers())
				// envtest has no controller-manager. Emulate its protection
				// controller only after verifying that no member Pods exist.
				pods := &corev1.PodList{}
				require.NoError(t, directClient.List(ctx, pods, client.InNamespace(ns.Name)))
				require.Empty(t, pods.Items)
				resource.SetFinalizers(nil)
				require.NoError(t, directClient.Update(ctx, resource))
			}
			require.Eventually(t, func() bool {
				if err := directClient.Get(ctx, client.ObjectKeyFromObject(resource), resource); err != nil {
					return false
				}
				return resource.GetUID() != uid && metav1.IsControlledBy(resource, gang)
			}, workloadReconcileTimeout, workloadPollInterval, "owner watch must recreate %T", resource)
		})
	}
	require.NoError(t, directClient.Get(ctx, client.ObjectKeyFromObject(gang), gang))
	assert.Equal(t, generation, gang.Generation)
	assert.Empty(t, gang.Status.Conditions, "backend must leave Pod-derived status to the PCS controller")
	leaf := &schedulingv1beta1.PodGroup{}
	require.NoError(t, directClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: leafName}, leaf))
	require.NotNil(t, leaf.Spec.SchedulingPolicy.Gang)
	assert.Equal(t, int32(2), leaf.Spec.SchedulingPolicy.Gang.MinCount)
	assert.Equal(t, ptr.To(childName), leaf.Spec.ParentCompositePodGroupName)
}
