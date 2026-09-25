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

package scaleguard

import (
	"context"
	"fmt"
	"testing"
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	testNamespace = "default"
	testPCSName   = "test-pcs"
)

// guardResource parameterizes the guard scenarios over each kind it protects. newEmpty returns the empty
// resource the guard fetches the owner into, and newTarget builds a labelled resource owned by testPCSName
// at the given PCS replica index.
type guardResource struct {
	kind      string
	newEmpty  func() client.Object
	newTarget func(pcsReplicaIndex int, minAvailable int32) client.Object
}

func TestHandle(t *testing.T) {
	resources := []guardResource{
		{
			kind:     "PodClique",
			newEmpty: func() client.Object { return &grovecorev1alpha1.PodClique{} },
			newTarget: func(pcsReplicaIndex int, minAvailable int32) client.Object {
				b := testutils.NewPodCliqueBuilder(testPCSName, "uid", "frontend", testNamespace, int32(pcsReplicaIndex))
				if minAvailable > 0 {
					b = b.WithMinAvailable(minAvailable)
				}
				return b.Build()
			},
		},
		{
			kind:     "PodCliqueScalingGroup",
			newEmpty: func() client.Object { return &grovecorev1alpha1.PodCliqueScalingGroup{} },
			newTarget: func(pcsReplicaIndex int, minAvailable int32) client.Object {
				b := testutils.NewPodCliqueScalingGroupBuilder("decode", testNamespace, testPCSName, pcsReplicaIndex)
				if minAvailable > 0 {
					b = b.WithMinAvailable(minAvailable)
				}
				return b.Build()
			},
		},
	}

	testCases := []struct {
		description               string
		operation                 admissionv1.Operation
		subResource               string
		oldReplicas               int32
		newReplicas               int32
		targetMinAvailable        int32
		targetReplicaIndex        int
		targetPresent             bool
		pcsPresent                bool
		pcsUpdatingReplicaIndices []int32
		wantAllowed               bool
	}{
		{
			description:   "an operation other than update is allowed",
			operation:     admissionv1.Create,
			targetPresent: true, pcsPresent: true, pcsUpdatingReplicaIndices: []int32{0},
			oldReplicas: 2, newReplicas: 4, wantAllowed: true,
		},
		{
			description:   "an update that does not change replicas is allowed",
			operation:     admissionv1.Update,
			targetPresent: true, pcsPresent: true, pcsUpdatingReplicaIndices: []int32{0},
			oldReplicas: 2, newReplicas: 2, wantAllowed: true,
		},
		{
			description:   "a replica change is allowed when no coherent update is in progress",
			operation:     admissionv1.Update,
			targetPresent: true, pcsPresent: true, pcsUpdatingReplicaIndices: nil,
			oldReplicas: 2, newReplicas: 4, wantAllowed: true,
		},
		{
			description:   "a replica change on the resource is rejected while a coherent update is in progress",
			operation:     admissionv1.Update,
			targetPresent: true, pcsPresent: true, pcsUpdatingReplicaIndices: []int32{0},
			oldReplicas: 2, newReplicas: 4, wantAllowed: false,
		},
		{
			description: "a replica change through the scale subresource is rejected while a coherent update is in progress",
			operation:   admissionv1.Update, subResource: "scale",
			targetPresent: true, pcsPresent: true, pcsUpdatingReplicaIndices: []int32{0},
			oldReplicas: 2, newReplicas: 4, wantAllowed: false,
		},
		{
			description:        "a replica change is rejected on a replica not itself under update while a coherent update is in progress",
			operation:          admissionv1.Update,
			targetReplicaIndex: 0, targetPresent: true, pcsPresent: true, pcsUpdatingReplicaIndices: []int32{1},
			oldReplicas: 2, newReplicas: 4, wantAllowed: false,
		},
		{
			description:   "a replica change is allowed when the target resource cannot be found",
			operation:     admissionv1.Update,
			targetPresent: false, pcsPresent: true, pcsUpdatingReplicaIndices: []int32{0},
			oldReplicas: 2, newReplicas: 4, wantAllowed: true,
		},
		{
			description:   "a replica change is allowed when the owning PodCliqueSet cannot be found",
			operation:     admissionv1.Update,
			targetPresent: true, pcsPresent: false,
			oldReplicas: 2, newReplicas: 4, wantAllowed: true,
		},
		{
			description:   "a replica change on the resource to below minAvailable is rejected",
			operation:     admissionv1.Update,
			targetPresent: true, pcsPresent: true, pcsUpdatingReplicaIndices: nil,
			oldReplicas: 4, newReplicas: 1, targetMinAvailable: 2, wantAllowed: false,
		},
		{
			description:   "a replica change on the resource to at least minAvailable is allowed",
			operation:     admissionv1.Update,
			targetPresent: true, pcsPresent: true, pcsUpdatingReplicaIndices: nil,
			oldReplicas: 4, newReplicas: 2, targetMinAvailable: 2, wantAllowed: true,
		},
		{
			description: "a scale to below the stored minAvailable is rejected",
			operation:   admissionv1.Update, subResource: "scale",
			targetPresent: true, pcsPresent: true, pcsUpdatingReplicaIndices: nil,
			oldReplicas: 4, newReplicas: 1, targetMinAvailable: 2, wantAllowed: false,
		},
		{
			description: "a scale to at least the stored minAvailable is allowed",
			operation:   admissionv1.Update, subResource: "scale",
			targetPresent: true, pcsPresent: true, pcsUpdatingReplicaIndices: nil,
			oldReplicas: 4, newReplicas: 2, targetMinAvailable: 2, wantAllowed: true,
		},
	}

	for _, resource := range resources {
		for _, tc := range testCases {
			t.Run(fmt.Sprintf("%s/%s", resource.kind, tc.description), func(t *testing.T) {
				target := resource.newTarget(tc.targetReplicaIndex, tc.targetMinAvailable)
				var objects []client.Object
				if tc.targetPresent {
					objects = append(objects, target)
				}
				if tc.pcsPresent {
					objects = append(objects, coherentPCS(tc.pcsUpdatingReplicaIndices))
				}
				cl := testutils.NewTestClientBuilder().WithObjects(objects...).Build()

				req := newRequest(tc.operation, tc.subResource, testNamespace, target.GetName(), tc.oldReplicas, tc.newReplicas)
				resp := Handle(context.Background(), req, cl, logr.Discard(), resource.newEmpty())

				assert.Equal(t, tc.wantAllowed, resp.Allowed)
			})
		}
	}
}

// TestMinAvailableOf checks that minAvailable is read from the stored PodClique or PodCliqueScalingGroup and
// that any other type yields nil.
func TestMinAvailableOf(t *testing.T) {
	testCases := []struct {
		description string
		target      client.Object
		want        *int32
	}{
		{
			description: "reads minAvailable from a PodClique",
			target:      &grovecorev1alpha1.PodClique{Spec: grovecorev1alpha1.PodCliqueSpec{MinAvailable: ptr.To[int32](3)}},
			want:        ptr.To[int32](3),
		},
		{
			description: "reads minAvailable from a PodCliqueScalingGroup",
			target:      &grovecorev1alpha1.PodCliqueScalingGroup{Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{MinAvailable: ptr.To[int32](2)}},
			want:        ptr.To[int32](2),
		},
		{
			description: "returns nil when minAvailable is unset",
			target:      &grovecorev1alpha1.PodClique{},
			want:        nil,
		},
		{
			description: "returns nil for an unexpected type",
			target:      &grovecorev1alpha1.PodCliqueSet{},
			want:        nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			assert.Equal(t, tc.want, minAvailableOf(tc.target))
		})
	}
}

func newRequest(operation admissionv1.Operation, subResource, namespace, name string, oldReplicas, newReplicas int32) admission.Request {
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation:   operation,
		Namespace:   namespace,
		Name:        name,
		SubResource: subResource,
		OldObject:   runtime.RawExtension{Raw: replicasJSON(oldReplicas)},
		Object:      runtime.RawExtension{Raw: replicasJSON(newReplicas)},
	}}
}

func replicasJSON(replicas int32) []byte {
	return []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas))
}

// coherentPCS builds a Coherent-strategy PodCliqueSet. When updatingReplicaIndices is non-nil an update is
// in flight with those replica indices in CurrentlyUpdating, UpdateEndedAt unset. A nil argument means no
// update is in progress.
func coherentPCS(updatingReplicaIndices []int32) *grovecorev1alpha1.PodCliqueSet {
	builder := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
		WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy})
	if updatingReplicaIndices != nil {
		currentlyUpdating := make([]grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress, 0, len(updatingReplicaIndices))
		for _, replicaIndex := range updatingReplicaIndices {
			currentlyUpdating = append(currentlyUpdating, grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{ReplicaIndex: replicaIndex})
		}
		builder = builder.WithUpdateProgress(&grovecorev1alpha1.PodCliqueSetUpdateProgress{
			UpdateStartedAt:   metav1.NewTime(time.Now()),
			CurrentlyUpdating: currentlyUpdating,
		})
	}
	return builder.Build()
}
