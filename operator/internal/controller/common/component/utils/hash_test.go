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

package utils

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestHashCandidates(t *testing.T) {
	t.Run("matches_canonical_or_legacy_hash", func(t *testing.T) {
		candidates := HashCandidates{
			Canonical: "canonical-hash",
			Legacy:    "legacy-hash",
		}

		assert.True(t, candidates.Matches("canonical-hash"))
		assert.True(t, candidates.Matches("legacy-hash"))
		assert.False(t, candidates.Matches("other-hash"))
		assert.False(t, candidates.IsLegacy("canonical-hash"))
		assert.True(t, candidates.IsLegacy("legacy-hash"))
		assert.False(t, candidates.IsLegacy("other-hash"))
	})

	t.Run("legacy_is_false_when_hashes_are_equal", func(t *testing.T) {
		candidates := HashCandidates{
			Canonical: "same-hash",
			Legacy:    "same-hash",
		}

		assert.True(t, candidates.Matches("same-hash"))
		assert.False(t, candidates.IsLegacy("same-hash"))
	})
}

func TestComputePCLQPodTemplateHashCandidates(t *testing.T) {
	template := hashTestPCLQTemplate("worker", corev1.Container{Name: "sidecar"}, corev1.Container{Name: "main"})

	candidates := ComputePCLQPodTemplateHashCandidates(template, "high-priority")

	assert.Equal(t, ComputePCLQPodTemplateHash(template, "high-priority"), candidates.Canonical)
	assert.Equal(t, ComputePCLQPodTemplateHashLegacy(template, "high-priority"), candidates.Legacy)
	assert.NotEqual(t, candidates.Canonical, candidates.Legacy,
		"the fixture stores a name-keyed slice out of canonical order so the legacy compatibility hash should diverge")
}

func TestGetExpectedPCLQPodTemplateHashCandidates(t *testing.T) {
	pcs := hashTestPCSWithCliques(nil,
		hashTestPCLQTemplate("worker", corev1.Container{Name: "sidecar"}, corev1.Container{Name: "main"}),
	)
	pcs.Spec.Template.PriorityClassName = "high-priority"

	candidates, err := GetExpectedPCLQPodTemplateHashCandidates(pcs, hashTestStandalonePCLQObjectMeta(pcs, "worker"))
	require.NoError(t, err)
	assert.Equal(t, ComputePCLQPodTemplateHashCandidates(pcs.Spec.Template.Cliques[0], "high-priority"), candidates)
	assert.NotEqual(t,
		ComputePCLQPodTemplateHashCandidates(pcs.Spec.Template.Cliques[0], "").Canonical,
		candidates.Canonical,
		"PCS priorityClassName must be included in the expected pod-template hash")

	_, err = GetExpectedPCLQPodTemplateHashCandidates(pcs, hashTestStandalonePCLQObjectMeta(pcs, "missing"))
	assert.Error(t, err)
}

func TestComputePCSGenerationHash_StartupOrderPolicy(t *testing.T) {
	tests := []struct {
		name      string
		startup   *grovecorev1alpha1.CliqueStartupType
		wantEqual bool
	}{
		{
			name:      "nil_startup_defaults_to_any_order",
			startup:   nil,
			wantEqual: true,
		},
		{
			name:      "any_order_sorts_cliques_by_name",
			startup:   ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder),
			wantEqual: true,
		},
		{
			name:      "explicit_startup_sorts_cliques_by_name",
			startup:   ptr.To(grovecorev1alpha1.CliqueStartupTypeExplicit),
			wantEqual: true,
		},
		{
			name:      "in_order_preserves_clique_sequence",
			startup:   ptr.To(grovecorev1alpha1.CliqueStartupTypeInOrder),
			wantEqual: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			first := hashTestPCSWithIdenticalCliqueSpecs(tc.startup, "app", "database")
			reordered := hashTestPCSWithIdenticalCliqueSpecs(tc.startup, "database", "app")

			hashFirst := ComputePCSGenerationHash(first)
			hashReordered := ComputePCSGenerationHash(reordered)
			if tc.wantEqual {
				assert.Equal(t, hashFirst, hashReordered)
			} else {
				assert.NotEqual(t, hashFirst, hashReordered,
					"InOrder hashes must include clique names as order keys even when the pod templates are otherwise identical")
			}
		})
	}
}

func TestComputePCSGenerationHashLegacy_PreservesStoredCliqueOrder(t *testing.T) {
	first := hashTestPCSWithCliques(nil,
		hashTestPCLQTemplate("app", corev1.Container{Name: "main", Image: "app:v1"}),
		hashTestPCLQTemplate("database", corev1.Container{Name: "main", Image: "database:v1"}),
	)
	reordered := hashTestPCSWithCliques(nil,
		hashTestPCLQTemplate("database", corev1.Container{Name: "main", Image: "database:v1"}),
		hashTestPCLQTemplate("app", corev1.Container{Name: "main", Image: "app:v1"}),
	)

	assert.Equal(t, ComputePCSGenerationHash(first), ComputePCSGenerationHash(reordered),
		"canonical default startup hashes should sort cliques by name")
	assert.NotEqual(t, ComputePCSGenerationHashLegacy(first), ComputePCSGenerationHashLegacy(reordered),
		"legacy hashes preserve the stored clique slice order for compatibility with v0.1.0-alpha.8")

	candidates := ComputePCSGenerationHashCandidates(reordered)
	assert.Equal(t, ComputePCSGenerationHash(reordered), candidates.Canonical)
	assert.Equal(t, ComputePCSGenerationHashLegacy(reordered), candidates.Legacy)
}

func hashTestPCLQTemplate(name string, containers ...corev1.Container) *grovecorev1alpha1.PodCliqueTemplateSpec {
	return &grovecorev1alpha1.PodCliqueTemplateSpec{
		Name: name,
		Spec: grovecorev1alpha1.PodCliqueSpec{
			PodSpec: corev1.PodSpec{
				Containers: containers,
			},
		},
	}
}

func hashTestPCSWithIdenticalCliqueSpecs(startup *grovecorev1alpha1.CliqueStartupType, cliqueNames ...string) *grovecorev1alpha1.PodCliqueSet {
	cliques := make([]*grovecorev1alpha1.PodCliqueTemplateSpec, 0, len(cliqueNames))
	for _, cliqueName := range cliqueNames {
		cliques = append(cliques, hashTestPCLQTemplate(cliqueName, corev1.Container{Name: "main", Image: "worker:v1"}))
	}
	return hashTestPCSWithCliques(startup, cliques...)
}

func hashTestPCSWithCliques(startup *grovecorev1alpha1.CliqueStartupType, cliques ...*grovecorev1alpha1.PodCliqueTemplateSpec) *grovecorev1alpha1.PodCliqueSet {
	return &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Replicas: 1,
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				StartupType: startup,
				Cliques:     cliques,
			},
		},
	}
}

func hashTestStandalonePCLQObjectMeta(pcs *grovecorev1alpha1.PodCliqueSet, cliqueName string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 0}, cliqueName),
		Namespace: pcs.Namespace,
		Labels: map[string]string{
			apicommon.LabelPartOfKey:                pcs.Name,
			apicommon.LabelPodCliqueSetReplicaIndex: "0",
		},
	}
}
