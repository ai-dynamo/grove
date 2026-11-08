package pod

// Benchmarks for checkAndRemovePodSchedulingGates, which runs once per PodClique sync.
// Each measured iteration is one full call over N gated pods whose PodGang records all N of
// them; fixtures are rebuilt outside the timer so only the call itself is measured.
//
// Two phases of the sync are covered:
//   - BenchmarkGateRemovalDependencyWait: the PodGang's dependency epoch is not scheduled yet,
//     so every gated pod is evaluated and skipped and no Patch calls are made. The measured
//     wall-clock is the gate-decision scan plus the function's fixed per-sync work.
//   - BenchmarkCheckAndRemovePodSchedulingGates: every pod can be ungated, so the call also
//     performs the concurrent Patch fan-out, which dominates the measured wall-clock.

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"
)

func BenchmarkGateRemovalDependencyWait(b *testing.B) {
	for _, n := range []int{4, 10, 50, 100, 250, 1000} {
		b.Run(fmt.Sprintf("gatedPods=%d", n), func(b *testing.B) {
			ctx := context.Background()
			tailPodGangName := podGangNameForEpoch(testTailEpoch)
			anchorPodGangName := podGangNameForEpoch(testAnchor0Epoch)
			podNames := make([]string, n)
			for i := range podNames {
				podNames[i] = fmt.Sprintf("bench-pod-%06d", i)
			}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				pods := make([]*corev1.Pod, 0, n)
				objects := make([]client.Object, 0, n+2)
				for _, name := range podNames {
					p := gatedPod(name, tailPodGangName)
					pods = append(pods, p)
					objects = append(objects, p)
				}
				tailPodGang := testutils.NewPodGangBuilder(tailPodGangName, testNamespace).
					WithLabels(map[string]string{
						apicommon.LabelPartOfKey:                testPCSName,
						apicommon.LabelPodCliqueSetReplicaIndex: "0",
						apicommon.LabelEpoch:                    testTailEpoch,
					}).
					WithPodGroupPods(testCliqueName, podNames...).
					Build()
				// The anchor PodGang the tail depends on exists but was never scheduled,
				// so every gated pod is scanned and skipped.
				anchorPodGang := testutils.NewPodGangBuilder(anchorPodGangName, testNamespace).
					WithLabels(map[string]string{
						apicommon.LabelPartOfKey:                testPCSName,
						apicommon.LabelPodCliqueSetReplicaIndex: "0",
						apicommon.LabelEpoch:                    testAnchor0Epoch,
					}).
					WithPodGroupPods(testCliqueName).
					Build()
				objects = append(objects, tailPodGang, anchorPodGang)
				cl := testutils.NewTestClientBuilder().WithObjects(objects...).Build()
				r := &_resource{client: cl}
				ss := gateRemovalSnapshot(pods, twoEpochPGM())
				b.StartTimer()
				skipped, err := r.checkAndRemovePodSchedulingGates(ctx, logr.Discard(), ss)
				b.StopTimer()
				if err != nil {
					b.Fatalf("checkAndRemovePodSchedulingGates: %v", err)
				}
				if len(skipped) != n {
					b.Fatalf("expected all %d pods skipped during dependency wait, %d skipped", n, len(skipped))
				}
			}
		})
	}
}

func BenchmarkCheckAndRemovePodSchedulingGates(b *testing.B) {
	for _, n := range []int{250, 1000, 2000, 10000} {
		b.Run(fmt.Sprintf("gatedPods=%d", n), func(b *testing.B) {
			ctx := context.Background()
			podGangName := podGangNameForEpoch(testAnchor0Epoch)
			podNames := make([]string, n)
			for i := range podNames {
				podNames[i] = fmt.Sprintf("bench-pod-%06d", i)
			}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				pods := make([]*corev1.Pod, 0, n)
				objects := make([]client.Object, 0, n+1)
				for _, name := range podNames {
					p := gatedPod(name, podGangName)
					pods = append(pods, p)
					objects = append(objects, p)
				}
				podGang := testutils.NewPodGangBuilder(podGangName, testNamespace).
					WithLabels(map[string]string{
						apicommon.LabelPartOfKey:                testPCSName,
						apicommon.LabelPodCliqueSetReplicaIndex: "0",
						apicommon.LabelEpoch:                    testAnchor0Epoch,
					}).
					WithPodGroupPods(testCliqueName, podNames...).
					Build()
				objects = append(objects, podGang)
				cl := testutils.NewTestClientBuilder().WithObjects(objects...).Build()
				r := &_resource{client: cl}
				ss := gateRemovalSnapshot(pods, pgmWithEntries(anchorEntry()))
				b.StartTimer()
				skipped, err := r.checkAndRemovePodSchedulingGates(ctx, logr.Discard(), ss)
				b.StopTimer()
				if err != nil {
					b.Fatalf("checkAndRemovePodSchedulingGates: %v", err)
				}
				if len(skipped) != 0 {
					b.Fatalf("expected all %d pods ungated, %d skipped", n, len(skipped))
				}
			}
		})
	}
}

// BenchmarkGateRemovalDependencyWaitMultiGang covers the 1:N shape introduced with coherent
// updates: one PodClique's gated pods spread across several anchor PodGangs. The pre-fix scan
// cost is per-gang quadratic (each pod scans only its own gang's PodReferences); the fix builds
// one name set per referenced gang per sync.
func BenchmarkGateRemovalDependencyWaitMultiGang(b *testing.B) {
	for _, cfg := range []struct {
		totalPods int
		gangs     int
	}{{1000, 4}, {2000, 4}, {1000, 10}} {
		b.Run(fmt.Sprintf("pods=%d_gangs=%d", cfg.totalPods, cfg.gangs), func(b *testing.B) {
			ctx := context.Background()
			anchorPodGangName := podGangNameForEpoch(testAnchor0Epoch)
			gangNames := make([]string, cfg.gangs)
			podNamesByGang := make([][]string, cfg.gangs)
			for g := 0; g < cfg.gangs; g++ {
				gangNames[g] = fmt.Sprintf("bench-gang-%d", g)
			}
			for i := 0; i < cfg.totalPods; i++ {
				g := i % cfg.gangs
				podNamesByGang[g] = append(podNamesByGang[g], fmt.Sprintf("bench-pod-%06d", i))
			}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				pods := make([]*corev1.Pod, 0, cfg.totalPods)
				objects := make([]client.Object, 0, cfg.totalPods+cfg.gangs+1)
				for g, gangName := range gangNames {
					for _, name := range podNamesByGang[g] {
						p := gatedPod(name, gangName)
						pods = append(pods, p)
						objects = append(objects, p)
					}
					gang := testutils.NewPodGangBuilder(gangName, testNamespace).
						WithLabels(map[string]string{
							apicommon.LabelPartOfKey:                testPCSName,
							apicommon.LabelPodCliqueSetReplicaIndex: "0",
							apicommon.LabelEpoch:                    testTailEpoch,
						}).
						WithPodGroupPods(testCliqueName, podNamesByGang[g]...).
						Build()
					objects = append(objects, gang)
				}
				// The anchor PodGang the tail epoch depends on exists but was never scheduled,
				// so every gated pod is scanned and skipped.
				anchorPodGang := testutils.NewPodGangBuilder(anchorPodGangName, testNamespace).
					WithLabels(map[string]string{
						apicommon.LabelPartOfKey:                testPCSName,
						apicommon.LabelPodCliqueSetReplicaIndex: "0",
						apicommon.LabelEpoch:                    testAnchor0Epoch,
					}).
					WithPodGroupPods(testCliqueName).
					Build()
				objects = append(objects, anchorPodGang)
				cl := testutils.NewTestClientBuilder().WithObjects(objects...).Build()
				r := &_resource{client: cl}
				ss := gateRemovalSnapshot(pods, twoEpochPGM())
				b.StartTimer()
				skipped, err := r.checkAndRemovePodSchedulingGates(ctx, logr.Discard(), ss)
				b.StopTimer()
				if err != nil {
					b.Fatalf("checkAndRemovePodSchedulingGates: %v", err)
				}
				if len(skipped) != cfg.totalPods {
					b.Fatalf("expected all %d pods skipped during dependency wait, %d skipped", cfg.totalPods, len(skipped))
				}
			}
		})
	}
}
