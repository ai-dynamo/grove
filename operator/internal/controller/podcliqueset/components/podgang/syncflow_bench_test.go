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

package podgang

import (
	"fmt"
	"testing"

	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
)

// BenchmarkReconcilePodGangStatus measures the real production method that evaluates
// arePodGangMinReplicasScheduled and arePodGangMinReplicasReady on every sync pass for every
// PodGang. The fixture is a single PodClique feeding one PodGang (the shape issue #577 calls
// out), held in steady state (conditions already current) so the timed loop measures the
// get + membership scan path, which is the per-reconcile cost every PodCliqueSet pays.
func BenchmarkReconcilePodGangStatus(b *testing.B) {
	cases := []struct {
		name           string
		totalPods      int // pods of the PodClique visible in the sync state
		associatedPods int // of those, pods already associated to this PodGang
	}{
		{"4pods", 4, 4},
		{"50pods", 50, 50},
		{"250pods", 250, 250},
		{"1000pods", 1000, 1000},
		{"4000pods", 4000, 4000},
		// 1:N shape: the PodClique's pods are spread across 4 PodGangs, this PodGang holds 250.
		{"1000total_250associated", 1000, 250},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			const (
				ns       = "default"
				pgName   = "bench-pcs-0-pg"
				pclqName = "bench-pcs-0-worker"
			)
			pcs := testutils.NewPodCliqueSetBuilder("bench-pcs", ns, "uid").Build()

			pods := make([]corev1.Pod, 0, tc.totalPods)
			associated := make([]string, 0, tc.associatedPods)
			for i := 0; i < tc.totalPods; i++ {
				name := fmt.Sprintf("worker-%05d", i)
				pods = append(pods, *testutils.NewPodBuilder(name, ns).
					WithCondition(corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}).
					WithCondition(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
					Build())
				if i < tc.associatedPods {
					associated = append(associated, name)
				}
			}
			pgi := &podGangInfo{fqn: pgName, pcsReplicaIndex: 0, pclqs: []pclqInfo{
				{fqn: pclqName, replicas: int32(tc.totalPods), minAvailable: int32(tc.associatedPods), associatedPodNames: associated},
			}}
			ss := &syncState{
				pcs:              pcs,
				existingPCLQPods: map[string][]corev1.Pod{pclqName: pods},
			}

			cl := testutils.NewTestClientBuilder().
				WithObjects(pcs, &groveschedulerv1alpha1.PodGang{ObjectMeta: metav1.ObjectMeta{Name: pgName, Namespace: ns}}).
				WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
				Build()
			r := &_resource{
				client:        cl,
				scheme:        groveclientscheme.Scheme,
				eventRecorder: record.NewFakeRecorder(10),
			}

			ctx := b.Context()
			// Warmup: let the method patch the PodGang into its canonical steady state, so
			// subsequent calls take the unchanged-status path (no patch), as in steady-state
			// production reconciles.
			if err := r.reconcilePodGangStatus(ctx, ss, pgi, true); err != nil {
				b.Fatalf("warmup reconcile failed: %v", err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := r.reconcilePodGangStatus(ctx, ss, pgi, true); err != nil {
					b.Fatalf("reconcile failed: %v", err)
				}
			}
		})
	}
}
