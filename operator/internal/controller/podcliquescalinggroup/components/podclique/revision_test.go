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

package podclique

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	"github.com/ai-dynamo/grove/operator/internal/utils/controllerrevision"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	testTargetRevision = "test-pcs-target"
	testForeignHash    = "hash-from-another-build"
)

// TestRestampUnchangedPCLQs verifies that a member whose clique has the same pod template in the target revision
// moves to the target revision without a new hash, while a member with another pod template keeps its revision so
// the update replaces it.
func TestRestampUnchangedPCLQs(t *testing.T) {
	pcs := newBuildResourcePCS("worker", nil, []corev1.Container{{Name: "c", Image: "v1"}}, nil)
	same := newPCSGTestRevision(t, pcs, "test-pcs-same", func(template *grovecorev1beta1.PodCliqueSetTemplateSpec) {
		template.Cliques[0].Spec.Replicas = 3
	})
	changed := newPCSGTestRevision(t, pcs, "test-pcs-changed", func(template *grovecorev1beta1.PodCliqueSetTemplateSpec) {
		template.Cliques[0].Spec.PodSpec.Containers[0].Image = "v0"
	})
	computedHash := componentutils.ComputePCLQPodTemplateHash(pcs.Spec.Template.Cliques[0], pcs.Spec.Template.PriorityClassName)
	pcsg := newBuildResourcePCSG(nil)
	pcsg.Labels[apicommon.LabelPodCliqueSetRevision] = testTargetRevision

	unchanged := newPCSGMember(pcsg, 0, same.Name, testForeignHash)
	outdated := newPCSGMember(pcsg, 1, changed.Name, "old-hash")
	legacy := newPCSGMember(pcsg, 2, "", computedHash)
	legacyOutdated := newPCSGMember(pcsg, 3, "", "old-hash")
	members := []grovecorev1beta1.PodClique{unchanged, outdated, legacy, legacyOutdated}
	objects := []client.Object{pcs, same, changed}
	for i := range members {
		objects = append(objects, members[i].DeepCopy())
	}
	cl := testutils.SetupFakeClient(objects...)
	r := _resource{client: cl}
	ss := &syncSnapshot{pcs: pcs, pcsg: pcsg, existingPCLQs: members, targetRevision: testTargetRevision}

	require.NoError(t, r.restampUnchangedPCLQs(t.Context(), logr.Discard(), ss))

	wantRevisions := map[string]string{
		unchanged.Name:      testTargetRevision,
		outdated.Name:       changed.Name,
		legacy.Name:         testTargetRevision,
		legacyOutdated.Name: "",
	}
	for i := range ss.existingPCLQs {
		pclq := ss.existingPCLQs[i]
		assert.Equal(t, wantRevisions[pclq.Name], pclq.Labels[apicommon.LabelPodCliqueSetRevision], "snapshot of %s", pclq.Name)
		stored := &grovecorev1beta1.PodClique{}
		require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(&pclq), stored))
		assert.Equal(t, wantRevisions[pclq.Name], stored.Labels[apicommon.LabelPodCliqueSetRevision], "stored %s", pclq.Name)
		assert.Equal(t, members[i].Labels[apicommon.LabelPodTemplateHash], stored.Labels[apicommon.LabelPodTemplateHash], "the hash of %s must not change", pclq.Name)
	}
}

// TestBuildResource_MemberPodTemplateHash verifies that a member PodClique written from the target revision keeps
// its hash when its clique's pod template is unchanged and gets a computed hash otherwise.
func TestBuildResource_MemberPodTemplateHash(t *testing.T) {
	pcs := newBuildResourcePCS("worker", nil, []corev1.Container{{Name: "c", Image: "v1"}}, nil)
	same := newPCSGTestRevision(t, pcs, "test-pcs-same", func(template *grovecorev1beta1.PodCliqueSetTemplateSpec) {
		template.Cliques[0].Spec.Replicas = 3
	})
	changed := newPCSGTestRevision(t, pcs, "test-pcs-changed", func(template *grovecorev1beta1.PodCliqueSetTemplateSpec) {
		template.Cliques[0].Spec.PodSpec.Containers[0].Image = "v0"
	})
	computedHash := componentutils.ComputePCLQPodTemplateHash(pcs.Spec.Template.Cliques[0], pcs.Spec.Template.PriorityClassName)
	pcsg := newBuildResourcePCSG(nil)
	pcsg.Labels[apicommon.LabelPodCliqueSetRevision] = testTargetRevision
	operator := &_resource{client: testutils.SetupFakeClient(pcs, same, changed), scheme: groveclientscheme.Scheme}

	tests := []struct {
		name     string
		pclq     *grovecorev1beta1.PodClique
		exists   bool
		wantHash string
	}{
		{name: "unchanged pod template keeps the hash", pclq: memberPtr(newPCSGMember(pcsg, 0, same.Name, testForeignHash)), exists: true, wantHash: testForeignHash},
		{name: "changed pod template gets a computed hash", pclq: memberPtr(newPCSGMember(pcsg, 0, changed.Name, testForeignHash)), exists: true, wantHash: computedHash},
		{name: "new member gets a computed hash", pclq: emptyMemberPodClique("test-pcs-0-sg-0-worker"), exists: false, wantHash: computedHash},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ss := &syncSnapshot{pcs: pcs, pcsg: pcsg, pcsReplicaIndex: 0, pgm: newAnchorPodGangMap(), targetRevision: testTargetRevision}

			require.NoError(t, operator.buildResource(t.Context(), logr.Discard(), ss, 0, tc.pclq, tc.exists))

			assert.Equal(t, tc.wantHash, tc.pclq.Labels[apicommon.LabelPodTemplateHash])
			assert.Equal(t, testTargetRevision, tc.pclq.Labels[apicommon.LabelPodCliqueSetRevision])
		})
	}
}

// newPCSGTestRevision returns a ControllerRevision of the PodCliqueSet that stores its template changed by mutate.
func newPCSGTestRevision(t *testing.T, pcs *grovecorev1beta1.PodCliqueSet, name string, mutate func(*grovecorev1beta1.PodCliqueSetTemplateSpec)) *appsv1.ControllerRevision {
	t.Helper()
	template := pcs.Spec.Template.DeepCopy()
	mutate(template)
	data, err := controllerrevision.EncodeTemplate(template)
	require.NoError(t, err)
	return &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       pcs.Namespace,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(pcs, grovecorev1beta1.SchemeGroupVersion.WithKind(apiconstants.KindPodCliqueSet))},
		},
		Data: runtime.RawExtension{Raw: data},
	}
}

// newPCSGMember returns a worker member PodClique of the PodCliqueScalingGroup replica with the given labels. An empty
// revision leaves the revision label out, as an older operator did.
func newPCSGMember(pcsg *grovecorev1beta1.PodCliqueScalingGroup, replicaIndex int, revision, podTemplateHash string) grovecorev1beta1.PodClique {
	labels := map[string]string{apicommon.LabelPodTemplateHash: podTemplateHash}
	if revision != "" {
		labels[apicommon.LabelPodCliqueSetRevision] = revision
	}
	name := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsg.Name, Replica: replicaIndex}, "worker")
	return *testutils.NewPCSGPodCliqueBuilder(name, pcsg.Namespace, "test-pcs", pcsg.Name, 0, replicaIndex).WithLabels(labels).Build()
}

func memberPtr(pclq grovecorev1beta1.PodClique) *grovecorev1beta1.PodClique {
	return &pclq
}
