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
	"encoding/json"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	commonrevision "github.com/ai-dynamo/grove/operator/internal/controller/common/revision"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// These identities were produced by the released hash function in a separate
// module using k8s.io/api and k8s.io/apimachinery v0.34.3.
const (
	legacyFrontendHash = "b5f67c599b4bf686777"
	legacyWorkerHash   = "86855bdb95cdb7bcd4f"
	legacyPairHash     = "b778ffcd948b74876f"
	legacyPriorityHash = "8d55dffc84c84cf4997"
)

func TestProcessRevisionAdoptsLegacyIdentity(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*grovecorev1alpha1.PodCliqueSet)
	}{
		{name: "unchanged workload"},
		{name: "scale before adoption", mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
			pcs.Spec.Replicas = 2
			pcs.Generation++
		}},
		{name: "scale to zero before adoption", mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
			pcs.Spec.Replicas = 0
			pcs.Generation++
		}},
		{name: "missing observed generation", mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
			pcs.Status.ObservedGeneration = nil
		}},
		{name: "existing update progress", mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
			pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{UpdateStartedAt: metav1.NewTime(time.Unix(1000, 0).UTC())}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pcs, _ := legacyRevisionFixture("worker")
			if tt.mutate != nil {
				tt.mutate(pcs)
			}
			progress := pcs.Status.UpdateProgress.DeepCopy()
			// No children are needed when the generation hash proves equivalence.
			cl := testutils.SetupFakeClient(pcs)
			r := &Reconciler{client: cl, apiReader: cl}
			data := processAndReadRevision(t, r, pcs)
			assert.Equal(t, legacyWorkerHash, data.Cliques[0].Hash)
			assert.Equal(t, legacyWorkerHash, data.GenerationHash)
			assert.True(t, equality.Semantic.DeepEqual(progress, pcs.Status.UpdateProgress), "adoption must preserve existing update progress")
			assert.EqualValues(t, 1, pcs.Status.UpdatedReplicas)

			selected := *pcs.Status.CurrentRevision
			// Restarting the controller must reload and retain the same identity.
			processAndReadRevision(t, &Reconciler{client: cl, apiReader: cl}, pcs)
			assert.Equal(t, selected, *pcs.Status.CurrentRevision)
		})
	}
}

func TestProcessRevisionAdoptsMixedLegacyReplicas(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("mixed", "test", "pcs-uid").
		WithReplicas(1).
		WithScalingGroupConfig("group", []string{"worker"}, 2, 1).
		Build()
	pcs.Generation = 2
	pcs.Spec.UpdateStrategy = &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.OnDeleteStrategy}
	pcs.Status.ObservedGeneration = ptr.To(pcs.Generation)
	pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers = []corev1.Container{{Name: "worker", Image: "worker:v2"}}
	desired, err := commonrevision.PodCliqueSetData(pcs)
	require.NoError(t, err)
	pcs.Status.CurrentGenerationHash = ptr.To(desired.GenerationHash)
	// OnDelete PCS sync can finish before the independent PCSG controller.
	pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{
		UpdateStartedAt: metav1.Now(),
		UpdateEndedAt:   ptr.To(metav1.Now()),
	}
	oldPCS := pcs.DeepCopy()
	oldPCS.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "worker:v1"
	pcsgName := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 0}, "group")
	pcsg := testutils.NewPodCliqueScalingGroupBuilder(pcsgName, pcs.Namespace, pcs.Name, 0).
		WithReplicas(2).
		WithCliqueNames([]string{"worker"}).
		WithOwnerReference("PodCliqueSet", pcs.Name, pcs.UID).
		Build()
	pcsg.UID = "pcsg-uid"
	objects := []client.Object{pcs, pcsg}
	var children []grovecorev1alpha1.PodClique
	for i, source := range []*grovecorev1alpha1.PodCliqueSet{pcs, oldPCS} {
		name := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsgName, Replica: i}, "worker")
		data, err := commonrevision.PodCliqueSetData(source)
		require.NoError(t, err)
		pclq := testutils.NewPCSGPodCliqueBuilder(name, pcs.Namespace, pcs.Name, pcsgName, 0, i).
			WithLabels(map[string]string{apicommon.LabelPodTemplateHash: data.Cliques[0].Hash}).
			WithOwnerReference("PodCliqueScalingGroup", pcsg.Name, pcsg.UID).
			Build()
		pclq.Spec = *source.Spec.Template.Cliques[0].Spec.DeepCopy()
		objects = append(objects, pclq)
		children = append(children, *pclq)
	}
	cl := testutils.SetupFakeClient(objects...)
	data := processAndReadRevision(t, &Reconciler{client: cl, apiReader: cl}, pcs)
	assert.Equal(t, desired.Cliques[0].Hash, data.Cliques[0].Hash)
	assert.Equal(t, desired.GenerationHash, data.GenerationHash)
	revision, err := componentutils.GetPodCliqueSetRevision(context.Background(), cl, pcs)
	require.NoError(t, err)
	assert.Equal(t, []string{children[1].Name}, componentutils.GetPCLQsInPCSGPendingUpdate(revision, pcs, pcsg, children))
}

func TestProcessRevisionRetainsUnchangedCliqueIdentity(t *testing.T) {
	for _, adoptFirst := range []bool{false, true} {
		name := "edit before adoption"
		if adoptFirst {
			name = "edit after adoption"
		}
		t.Run(name, func(t *testing.T) {
			pcs, objects := legacyRevisionFixture("frontend", "worker")
			cl := testutils.SetupFakeClient(objects...)
			r := &Reconciler{client: cl, apiReader: cl}
			if adoptFirst {
				processAndReadRevision(t, r, pcs)
			}
			pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "frontend:v2"
			pcs.Generation++
			require.NoError(t, cl.Update(context.Background(), pcs))
			after := processAndReadRevision(t, r, pcs)
			assert.Equal(t, legacyWorkerHash, after.Cliques[1].Hash)
			assert.NotEqual(t, legacyFrontendHash, after.Cliques[0].Hash)
			assert.NotEqual(t, legacyPairHash, after.GenerationHash)
			require.NotNil(t, pcs.Status.UpdateProgress)
			assert.Nil(t, pcs.Status.UpdateProgress.UpdateEndedAt)
			assert.Zero(t, pcs.Status.UpdatedReplicas)
		})
	}
}

func TestProcessRevisionAdoptsExplicitPodPriority(t *testing.T) {
	pcs, objects := legacyRevisionFixture("worker")
	pcs.Spec.Template.PriorityClassName = "gang-priority"
	pcs.Spec.Template.Cliques[0].Spec.PodSpec.PriorityClassName = "pod-priority"
	pcs.Status.CurrentGenerationHash = ptr.To(legacyPriorityHash)
	objects[1].(*grovecorev1alpha1.PodClique).Spec.PodSpec.PriorityClassName = "pod-priority"
	objects[1].(*grovecorev1alpha1.PodClique).Labels[apicommon.LabelPodTemplateHash] = legacyPriorityHash
	cl := testutils.SetupFakeClient(objects...)
	r := &Reconciler{client: cl, apiReader: cl}
	before := processAndReadRevision(t, r, pcs)
	selected := *pcs.Status.CurrentRevision
	var template corev1.PodTemplateSpec
	require.NoError(t, json.Unmarshal(before.Cliques[0].Template, &template))
	assert.Equal(t, "pod-priority", template.Spec.PriorityClassName)
	assert.Equal(t, legacyPriorityHash, before.Cliques[0].Hash)

	after := processAndReadRevision(t, r, pcs)
	assert.Equal(t, selected, *pcs.Status.CurrentRevision)
	assert.Equal(t, before.Cliques[0].Hash, after.Cliques[0].Hash)
	assert.Nil(t, pcs.Status.UpdateProgress)
}

func TestProcessRevisionRejectsUnverifiableLegacyIdentity(t *testing.T) {
	pcs, objects := legacyRevisionFixture("worker")
	pcs.Status.CurrentGenerationHash = ptr.To("unknown-legacy-generation")
	cl := testutils.SetupFakeClient(objects...)
	r := &Reconciler{client: cl, apiReader: cl}
	result := r.processRevision(context.Background(), logr.Discard(), pcs)
	require.True(t, result.HasErrors())
	_, err := result.Result()
	require.ErrorContains(t, err, "cannot verify legacy generation hash")
	assert.Nil(t, pcs.Status.CurrentRevision)
	revisions := &appsv1.ControllerRevisionList{}
	require.NoError(t, cl.List(context.Background(), revisions))
	assert.Empty(t, revisions.Items)
}

func TestProcessRevisionRejectsAmbiguousLegacyCliqueIdentity(t *testing.T) {
	pcs, objects := legacyRevisionFixture("frontend", "worker")
	current, err := commonrevision.PodCliqueSetData(pcs)
	require.NoError(t, err)
	duplicate := objects[2].(*grovecorev1alpha1.PodClique).DeepCopy()
	duplicate.Name = pcs.Name + "-1-worker"
	duplicate.Labels[apicommon.LabelPodTemplateHash] = current.Cliques[1].Hash
	objects = append(objects, duplicate)
	pcs.Generation++
	pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "frontend:v2"
	cl := testutils.SetupFakeClient(objects...)
	r := &Reconciler{client: cl, apiReader: cl}
	result := r.processRevision(context.Background(), logr.Discard(), pcs)
	require.True(t, result.HasErrors())
	_, err = result.Result()
	require.ErrorContains(t, err, "multiple legacy identities for clique worker")
	assert.Nil(t, pcs.Status.CurrentRevision)
}

func TestProcessRevisionAdoptionUsesUncachedReader(t *testing.T) {
	pcs, objects := legacyRevisionFixture("frontend", "worker")
	pcs.Generation++
	pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "frontend:v2"
	cl := testutils.SetupFakeClient(pcs)
	apiReader := testutils.SetupFakeClient(objects...)
	after := processAndReadRevision(t, &Reconciler{client: cl, apiReader: apiReader}, pcs)
	assert.Equal(t, legacyWorkerHash, after.Cliques[1].Hash)
}

func TestProcessRevisionAdoptionPropagatesReadErrors(t *testing.T) {
	pcs, objects := legacyRevisionFixture("frontend", "worker")
	pcs.Generation++
	pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "frontend:v2"
	cl := testutils.SetupFakeClient(objects...)
	readError := assert.AnError
	apiReader := interceptor.NewClient(cl, interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return readError
		},
	})
	result := (&Reconciler{client: cl, apiReader: apiReader}).processRevision(context.Background(), logr.Discard(), pcs)
	require.True(t, result.HasErrors())
	_, err := result.Result()
	require.ErrorIs(t, err, readError)
	assert.Nil(t, pcs.Status.CurrentRevision)
}

func TestExistingPodTemplateHashesChecksOwnership(t *testing.T) {
	pcs, objects := legacyRevisionFixture("worker")
	standalone := objects[1].(*grovecorev1alpha1.PodClique)
	standalone.UID = "standalone-uid"
	pcsg := testutils.NewPodCliqueScalingGroupBuilder("group", pcs.Namespace, pcs.Name, 0).
		WithOwnerReference("PodCliqueSet", pcs.Name, pcs.UID).
		Build()
	pcsg.UID = "pcsg-uid"
	member := standalone.DeepCopy()
	member.Name, member.UID = "member", "member-uid"
	member.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(pcsg, grovecorev1alpha1.SchemeGroupVersion.WithKind("PodCliqueScalingGroup"))}
	member.Labels[apicommon.LabelPodTemplateHash] = "member-hash"
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "pod", Namespace: pcs.Namespace,
		Labels:          apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(member, grovecorev1alpha1.SchemeGroupVersion.WithKind("PodClique"))},
	}}
	pod.Labels[apicommon.LabelPodTemplateHash] = "pod-hash"
	foreignGroup := pcsg.DeepCopy()
	foreignGroup.Name, foreignGroup.UID = "foreign-group", "foreign-group-uid"
	foreignGroup.OwnerReferences[0].UID = "another-pcs"
	foreignMember := member.DeepCopy()
	foreignMember.Name, foreignMember.UID = "foreign-member", "foreign-member-uid"
	foreignMember.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(foreignGroup, grovecorev1alpha1.SchemeGroupVersion.WithKind("PodCliqueScalingGroup"))}
	foreignMember.Labels[apicommon.LabelPodTemplateHash] = "foreign-member-hash"
	foreignPod := pod.DeepCopy()
	foreignPod.Name = "foreign-pod"
	foreignPod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(foreignMember, grovecorev1alpha1.SchemeGroupVersion.WithKind("PodClique"))}
	foreignPod.Labels[apicommon.LabelPodTemplateHash] = "foreign-pod-hash"
	orphan := standalone.DeepCopy()
	orphan.Name, orphan.UID = "orphan", "orphan-uid"
	orphan.OwnerReferences = nil
	orphan.Labels[apicommon.LabelPodTemplateHash] = "orphan-hash"
	objects = append(objects, pcsg, member, pod, foreignGroup, foreignMember, foreignPod, orphan)
	cl := testutils.SetupFakeClient(objects...)
	hashes, err := (&Reconciler{client: cl, apiReader: cl}).existingPodTemplateHashes(context.Background(), pcs)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{legacyWorkerHash, "member-hash", "pod-hash"}, hashes.UnsortedList())
}

func legacyRevisionFixture(names ...string) (*grovecorev1alpha1.PodCliqueSet, []client.Object) {
	builder := testutils.NewPodCliqueSetBuilder("survivor", "test", "pcs-uid").WithReplicas(1)
	for _, name := range names {
		builder.WithPodCliqueParameters(name, 1, nil)
	}
	pcs := builder.Build()
	pcs.Generation = 1
	pcs.Status.ObservedGeneration = ptr.To(pcs.Generation)
	pcs.Status.CurrentGenerationHash = ptr.To(legacyWorkerHash)
	if len(names) == 2 {
		pcs.Status.CurrentGenerationHash = ptr.To(legacyPairHash)
	}
	pcs.Status.UpdatedReplicas = 1
	objects := []client.Object{pcs}
	for _, clique := range pcs.Spec.Template.Cliques {
		clique.Spec.PodSpec = corev1.PodSpec{Containers: []corev1.Container{{Name: clique.Name, Image: clique.Name + ":v1"}}}
		hash := legacyWorkerHash
		if clique.Name == "frontend" {
			hash = legacyFrontendHash
		}
		pclq := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, clique.Name, pcs.Namespace, 0).
			WithLabels(map[string]string{apicommon.LabelPodTemplateHash: hash}).
			Build()
		pclq.Spec = *clique.Spec.DeepCopy()
		objects = append(objects, pclq)
	}
	return pcs, objects
}

func processAndReadRevision(t *testing.T, r *Reconciler, pcs *grovecorev1alpha1.PodCliqueSet) commonrevision.Data {
	t.Helper()
	ctx := componentutils.WithPodCliqueSetRevisionCache(context.Background())
	result := r.processRevision(ctx, logr.Discard(), pcs)
	require.False(t, result.HasErrors(), "processRevision failed: %v", result.GetErrors())
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(pcs), pcs))
	require.NotNil(t, pcs.Status.CurrentRevision)
	revision := &appsv1.ControllerRevision{}
	require.NoError(t, r.client.Get(ctx, client.ObjectKey{Namespace: pcs.Namespace, Name: *pcs.Status.CurrentRevision}, revision))
	var data commonrevision.Data
	require.NoError(t, json.Unmarshal(revision.Data.Raw, &data))
	return data
}
