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

package podcliquesetreplica

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestIsPCLQUpdateComplete(t *testing.T) {
	const (
		wantTemplateHash = "tmpl-new"
		wantPCSGenHash   = "gen-new"
	)
	// basePCLQ returns a PodClique satisfying ALL five predicates, built via the test builder; each
	// test case mutates one field to drive a single predicate false.
	basePCLQ := func() *grovecorev1alpha1.PodClique {
		return testutils.NewPodCliqueBuilder("pcs", "uid", "worker", "default", 0).
			WithLabels(map[string]string{apicommon.LabelPodTemplateHash: wantTemplateHash}).
			WithMinAvailable(2).
			WithStatusCurrentPodTemplateHash(ptr.To(wantTemplateHash)).
			WithStatusCurrentPodCliqueSetGenerationHash(ptr.To(wantPCSGenHash)).
			WithStatusUpdatedReplicas(2).
			WithStatusReadyReplicas(2).
			Build()
	}
	tests := []struct {
		name   string
		mutate func(p *grovecorev1alpha1.PodClique)
		want   bool
	}{
		{name: "all predicates satisfied", mutate: func(*grovecorev1alpha1.PodClique) {}, want: true},
		{name: "Labels[pod-template-hash] mismatch", mutate: func(p *grovecorev1alpha1.PodClique) { p.Labels[apicommon.LabelPodTemplateHash] = "stale" }, want: false},
		{name: "Status.CurrentPodTemplateHash nil", mutate: func(p *grovecorev1alpha1.PodClique) { p.Status.CurrentPodTemplateHash = nil }, want: false},
		{name: "Status.CurrentPodTemplateHash mismatch", mutate: func(p *grovecorev1alpha1.PodClique) { p.Status.CurrentPodTemplateHash = ptr.To("stale") }, want: false},
		{name: "Status.CurrentPodCliqueSetGenerationHash nil", mutate: func(p *grovecorev1alpha1.PodClique) { p.Status.CurrentPodCliqueSetGenerationHash = nil }, want: false},
		{name: "Status.CurrentPodCliqueSetGenerationHash mismatch", mutate: func(p *grovecorev1alpha1.PodClique) { p.Status.CurrentPodCliqueSetGenerationHash = ptr.To("stale") }, want: false},
		{name: "Status.UpdatedReplicas below MinAvailable", mutate: func(p *grovecorev1alpha1.PodClique) { p.Status.UpdatedReplicas = 1 }, want: false},
		{name: "Status.ReadyReplicas below MinAvailable", mutate: func(p *grovecorev1alpha1.PodClique) { p.Status.ReadyReplicas = 1 }, want: false},
		{name: "Status.UpdatedReplicas and ReadyReplicas above MinAvailable is fine", mutate: func(p *grovecorev1alpha1.PodClique) { p.Status.UpdatedReplicas = 5; p.Status.ReadyReplicas = 5 }, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pclq := basePCLQ()
			tc.mutate(pclq)
			assert.Equal(t, tc.want, isPCLQUpdateComplete(pclq, wantTemplateHash, wantPCSGenHash))
		})
	}
}

func TestComputeUpdateProgress(t *testing.T) {
	const genHash = "gen-new"

	// pcsWith builds a PCS with the given standalone clique names and PCSG config names (each PCSG owns
	// one clique "<pcsg>-worker"). CurrentGenerationHash is set unless noHash.
	pcsWith := func(standalone, pcsgs []string, noHash bool) *grovecorev1alpha1.PodCliqueSet {
		cliques := make([]*grovecorev1alpha1.PodCliqueTemplateSpec, 0, len(standalone)+len(pcsgs))
		for _, c := range standalone {
			cliques = append(cliques, &grovecorev1alpha1.PodCliqueTemplateSpec{Name: c, Spec: grovecorev1alpha1.PodCliqueSpec{MinAvailable: ptr.To[int32](1)}})
		}
		var pcsgConfigs []grovecorev1alpha1.PodCliqueScalingGroupConfig
		for _, g := range pcsgs {
			member := g + "-worker"
			cliques = append(cliques, &grovecorev1alpha1.PodCliqueTemplateSpec{Name: member, Spec: grovecorev1alpha1.PodCliqueSpec{MinAvailable: ptr.To[int32](1)}})
			pcsgConfigs = append(pcsgConfigs, grovecorev1alpha1.PodCliqueScalingGroupConfig{Name: g, CliqueNames: []string{member}, MinAvailable: ptr.To[int32](1)})
		}
		pcs := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: "pcs", Namespace: "default"},
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{Cliques: cliques, PodCliqueScalingGroupConfigs: pcsgConfigs},
			},
		}
		if !noHash {
			pcs.Status.CurrentGenerationHash = ptr.To(genHash)
		}
		return pcs
	}

	// updatedStandalonePCLQ builds a standalone PodClique (FQN pcs-0-<clique>) fully converged on pcs.
	updatedStandalonePCLQ := func(t *testing.T, pcs *grovecorev1alpha1.PodCliqueSet, clique string) grovecorev1alpha1.PodClique {
		t.Helper()
		pclq := testutils.NewPodCliqueBuilder("pcs", "uid", clique, "default", 0).WithMinAvailable(1).Build()
		hash, err := componentutils.GetExpectedPCLQPodTemplateHash(pcs, pclq.ObjectMeta)
		require.NoError(t, err)
		return *testutils.NewPodCliqueBuilder("pcs", "uid", clique, "default", 0).
			WithLabels(map[string]string{apicommon.LabelPodTemplateHash: hash}).
			WithMinAvailable(1).
			WithStatusCurrentPodTemplateHash(ptr.To(hash)).
			WithStatusCurrentPodCliqueSetGenerationHash(ptr.To(genHash)).
			WithStatusUpdatedReplicas(1).
			WithStatusReadyReplicas(1).
			Build()
	}
	// stalePCLQ builds a standalone PodClique that has not converged (stale hashes).
	stalePCLQ := func(clique string) grovecorev1alpha1.PodClique {
		return *testutils.NewPodCliqueBuilder("pcs", "uid", clique, "default", 0).
			WithLabels(map[string]string{apicommon.LabelPodTemplateHash: "stale"}).
			WithMinAvailable(1).
			WithStatusCurrentPodTemplateHash(ptr.To("stale")).
			WithStatusCurrentPodCliqueSetGenerationHash(ptr.To("stale")).
			WithStatusUpdatedReplicas(1).
			WithStatusReadyReplicas(1).
			Build()
	}
	// updatedPCSG builds a PodCliqueScalingGroup converged on genHash (IsPCSGUpdateComplete only checks
	// Status.CurrentPodCliqueSetGenerationHash).
	updatedPCSG := func(name string) grovecorev1alpha1.PodCliqueScalingGroup {
		return *testutils.NewPodCliqueScalingGroupBuilder("pcs-0-"+name, "default", "pcs", 0).
			WithStatusCurrentPodCliqueSetGenerationHash(ptr.To(genHash)).Build()
	}

	tests := []struct {
		name     string
		pcs      *grovecorev1alpha1.PodCliqueSet
		build    func(t *testing.T, pcs *grovecorev1alpha1.PodCliqueSet) ([]grovecorev1alpha1.PodClique, []grovecorev1alpha1.PodCliqueScalingGroup)
		wantDone bool
	}{
		{
			name: "nil CurrentGenerationHash leaves updateDone false",
			pcs:  pcsWith([]string{"frontend"}, nil, true),
			build: func(*testing.T, *grovecorev1alpha1.PodCliqueSet) ([]grovecorev1alpha1.PodClique, []grovecorev1alpha1.PodCliqueScalingGroup) {
				return nil, nil
			},
			wantDone: false,
		},
		{
			name: "all standalone updated, no PCSGs => done",
			pcs:  pcsWith([]string{"frontend", "backend"}, nil, false),
			build: func(t *testing.T, pcs *grovecorev1alpha1.PodCliqueSet) ([]grovecorev1alpha1.PodClique, []grovecorev1alpha1.PodCliqueScalingGroup) {
				return []grovecorev1alpha1.PodClique{updatedStandalonePCLQ(t, pcs, "frontend"), updatedStandalonePCLQ(t, pcs, "backend")}, nil
			},
			wantDone: true,
		},
		{
			name: "one standalone stale => not done",
			pcs:  pcsWith([]string{"frontend", "backend"}, nil, false),
			build: func(t *testing.T, pcs *grovecorev1alpha1.PodCliqueSet) ([]grovecorev1alpha1.PodClique, []grovecorev1alpha1.PodCliqueScalingGroup) {
				return []grovecorev1alpha1.PodClique{updatedStandalonePCLQ(t, pcs, "frontend"), stalePCLQ("backend")}, nil
			},
			wantDone: false,
		},
		{
			name: "all PCSGs updated, no standalone => done",
			pcs:  pcsWith(nil, []string{"prefill"}, false),
			build: func(*testing.T, *grovecorev1alpha1.PodCliqueSet) ([]grovecorev1alpha1.PodClique, []grovecorev1alpha1.PodCliqueScalingGroup) {
				return nil, []grovecorev1alpha1.PodCliqueScalingGroup{updatedPCSG("prefill")}
			},
			wantDone: true,
		},
		{
			name: "one PCSG missing => not done",
			pcs:  pcsWith(nil, []string{"prefill", "decode"}, false),
			build: func(*testing.T, *grovecorev1alpha1.PodCliqueSet) ([]grovecorev1alpha1.PodClique, []grovecorev1alpha1.PodCliqueScalingGroup) {
				return nil, []grovecorev1alpha1.PodCliqueScalingGroup{updatedPCSG("prefill")}
			},
			wantDone: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pclqs, pcsgs := tc.build(t, tc.pcs)
			pri := &pcsReplicaInfo{replicaIndex: 0, pclqs: pclqs, pcsgs: pcsgs}
			pri.computeUpdateProgress(tc.pcs)
			assert.Equal(t, tc.wantDone, pri.updateDone)
		})
	}
}

func TestMarkCurrentReplicaUpdateDone(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pcs", Namespace: "default"},
		Status: grovecorev1alpha1.PodCliqueSetStatus{
			UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{
					{ReplicaIndex: 0, InFlightEpochs: []string{"100"}},
				},
			},
		},
	}
	cl := testutils.NewTestClientBuilder().WithObjects(pcs).WithStatusSubresource(pcs).Build()
	r := _resource{client: cl}

	require.NoError(t, r.markCurrentReplicaUpdateDone(context.Background(), logr.Discard(), pcs))

	progress := pcs.Status.UpdateProgress.CurrentlyUpdating[0]
	assert.NotNil(t, progress.UpdateEndedAt)
	assert.Nil(t, progress.InFlightEpochs)
}
