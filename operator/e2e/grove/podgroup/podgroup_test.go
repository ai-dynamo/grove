//go:build e2e

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

package podgroup

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	kaischedulingv2alpha2 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/scheduling/v2alpha2"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestFilterReplicaPodGroupForPCSReplicaIgnoresHistoricalPodGroups(t *testing.T) {
	owner := metav1.OwnerReference{Kind: "PodCliqueSet", Name: "job", Controller: ptr.To(true)}
	podGroups := []kaischedulingv2alpha2.PodGroup{
		{ObjectMeta: metav1.ObjectMeta{
			Name:            "historical",
			Labels:          map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "0", apicommon.LabelComponentKey: apicommon.LabelComponentNamePodGang},
			OwnerReferences: []metav1.OwnerReference{owner},
		}},
		{ObjectMeta: metav1.ObjectMeta{
			Name:            "replica-podgroup",
			Labels:          map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "0", apicommon.LabelComponentKey: apicommon.LabelComponentNamePodCliqueSetReplicaPodGroup},
			OwnerReferences: []metav1.OwnerReference{owner},
		}},
	}

	replicaPodGroup, err := FilterReplicaPodGroupForPCSReplica(podGroups, "job", 0)
	require.NoError(t, err)
	require.Equal(t, "replica-podgroup", replicaPodGroup.Name)
}

func TestExpectedReplicaPodGroupSubGroupShapesMatchesRoleAwareHierarchy(t *testing.T) {
	anchorBranch := "podgang-1000"
	collection := "non-anchors"
	wrapper := "non-anchor-2000-workers-1"
	scaledBranch := "podgang-2000-workers-1"
	topologyGroup := "rack-group"
	actual, err := subGroupShapes([]kaischedulingv2alpha2.SubGroup{
		{Name: anchorBranch, MinSubGroup: ptr.To[int32](1)},
		{Name: "anchor-leaf", Parent: &anchorBranch, MinMember: ptr.To[int32](2), TopologyConstraint: topologyConstraint("host", "")},
		{Name: collection, MinSubGroup: ptr.To[int32](1)},
		{Name: wrapper, Parent: &collection, MinSubGroup: ptr.To[int32](0)},
		{Name: scaledBranch, Parent: &wrapper, MinSubGroup: ptr.To[int32](1)},
		{Name: topologyGroup, Parent: &scaledBranch, MinSubGroup: ptr.To[int32](1), TopologyConstraint: topologyConstraint("rack", "")},
		{Name: "scaled-leaf", Parent: &topologyGroup, MinMember: ptr.To[int32](1)},
	})
	require.NoError(t, err)

	expected, err := expectedReplicaPodGroupSubGroupShapes(
		"job",
		0,
		[]ExpectedSubGroup{CreateExpectedStandalonePCLQSubGroup("job", 0, "worker", 2, "host")},
		[]ScaledPCSGConfig{{
			PCSGName:    "workers",
			PCSGReplica: 1,
			Constraint:  "rack",
			CliqueConfigs: []PCSGCliqueConfig{
				{Name: "worker", PodCount: 1},
			},
		}},
	)
	require.NoError(t, err)
	require.Equal(t, canonicalShapes(expected), canonicalShapes(actual))
}

func topologyConstraint(required, preferred string) *kaischedulingv2alpha2.TopologyConstraint {
	return &kaischedulingv2alpha2.TopologyConstraint{
		RequiredTopologyLevel:  required,
		PreferredTopologyLevel: preferred,
	}
}
