//go:build e2e

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

package update

import (
	"testing"

	tests "github.com/ai-dynamo/grove/operator/e2e/tests"
)

const (
	coherentWorkloadName = "workload-mvu-test1"
	coherentWorkloadYAML = "../../yaml/workload-mvu-test1.yaml"
	coherentWorkerNodes  = 27
	// Initial pod count: frontend=3 + prefill(4 replicas × 2 pclqs)=8 + decode(5 replicas × 2 pclqs)=10 = 21
	coherentExpectedPods = 21
)

// Test_CU1_MVUPodGangMapLifecycle exercises PCS1 through the full lifecycle during a coherent update
//
// PCS1 spec: frontend (standalone, 3 replicas/minAvailable=1), pleader/pworker/dleader/dworker
// (each 1 replica/minAvailable=1), PCSG prefill (4 replicas/minAvailable=2 over pleader+pworker),
// PCSG decode (5 replicas/minAvailable=2 over dleader+dworker).
// MVU template: {frontend:1, prefill:2, decode:2}
//
// All PodGangMap state assertions use multiset equality on entry compositions, ignoring
// tail-PG name ordering (tail PG names are not stable across reconciles).
func Test_CU1_MVUPodGangMapLifecycle(t *testing.T) {
	const pcsName = coherentWorkloadName

	// -----------------------------------------------------------------------
	// Steps 1–2: Deploy PCS1 and verify initial PodGangMap.
	// Expected PGM: {F:1,P:2,D:2}, {F:2,P:2,D:2}, {D:1}
	// -----------------------------------------------------------------------
	tests.Logger.Info("Step 1: Deploy PCS1 (workload-mvu-test1), verify 21 pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: coherentWorkloadName,
		workloadYAML: coherentWorkloadYAML,
		workerNodes:  coherentWorkerNodes,
		expectedPods: coherentExpectedPods,
	})
	defer cleanup()

	tests.Logger.Info("Step 2: Verify initial PodGangMap — {F:1,P:2,D:2}, {F:2,P:2,D:2}, {D:1}")
	if err := waitForPGMComposition(tc, pcsName, 0, []pgComposition{
		pg(map[string]int32{"frontend": 1}, map[string]int32{"prefill": 2, "decode": 2}),
		pg(map[string]int32{"frontend": 2}, map[string]int32{"prefill": 2, "decode": 2}),
		pg(nil, map[string]int32{"decode": 1}),
	}); err != nil {
		t.Fatalf("Step 2 PGM mismatch: %v", err)
	}

	// -----------------------------------------------------------------------
	// Steps 3–4: Scale out prefill PCSG 4 → 6.
	// New total pods: 21 + 4 = 25 (2 new replicas × 2 pclqs each)
	// Expected PGM: {F:1,P:2,D:2}, {F:2,P:2,D:2}, {D:1}, {P:1}, {P:1}
	// -----------------------------------------------------------------------
	tests.Logger.Info("Step 3: Scale out prefill PCSG 4 → 6 (expect 25 pods)")
	tc.ScalePCSGAcrossAllReplicasAndWait(pcsName, "prefill", 1, 6, 25, 0)

	tests.Logger.Info("Step 4: Verify PGM after prefill scale-out")
	if err := waitForPGMComposition(tc, pcsName, 0, []pgComposition{
		pg(map[string]int32{"frontend": 1}, map[string]int32{"prefill": 2, "decode": 2}),
		pg(map[string]int32{"frontend": 2}, map[string]int32{"prefill": 2, "decode": 2}),
		pg(nil, map[string]int32{"decode": 1}),
		pg(nil, map[string]int32{"prefill": 1}),
		pg(nil, map[string]int32{"prefill": 1}),
	}); err != nil {
		t.Fatalf("Step 4 PGM mismatch: %v", err)
	}

	// -----------------------------------------------------------------------
	// Steps 5–6: Coherent update of frontend, pleader, and dleader.
	// Multiset is unchanged from step 4 (tail PG names may reorder but not compositions).
	// -----------------------------------------------------------------------
	tests.Logger.Info("Step 5: Trigger coherent update of frontend, pleader, dleader")
	for _, clique := range []string{"frontend", "pleader", "dleader"} {
		if err := triggerPodCliqueUpdate(tc, clique); err != nil {
			t.Fatalf("Failed to trigger update on %s: %v", clique, err)
		}
	}
	if err := waitForCoherentUpdateComplete(tc, 1); err != nil {
		t.Fatalf("Step 5 coherent update did not complete: %v", err)
	}

	tests.Logger.Info("Step 6: Verify PGM after update — same multiset as step 4")
	if err := waitForPGMComposition(tc, pcsName, 0, []pgComposition{
		pg(map[string]int32{"frontend": 1}, map[string]int32{"prefill": 2, "decode": 2}),
		pg(map[string]int32{"frontend": 2}, map[string]int32{"prefill": 2, "decode": 2}),
		pg(nil, map[string]int32{"decode": 1}),
		pg(nil, map[string]int32{"prefill": 1}),
		pg(nil, map[string]int32{"prefill": 1}),
	}); err != nil {
		t.Fatalf("Step 6 PGM mismatch: %v", err)
	}

	// -----------------------------------------------------------------------
	// Steps 7–8: Scale out decode PCSG 5 → 6.
	// New total pods: 25 + 2 = 27 (1 new replica × 2 pclqs)
	// Expected PGM: {F:1,P:2,D:2}, {F:2,P:2,D:2}, {P:1}, {P:1}, {D:1}, {D:1}
	// -----------------------------------------------------------------------
	tests.Logger.Info("Step 7: Scale out decode PCSG 5 → 6 (expect 27 pods)")
	tc.ScalePCSGAcrossAllReplicasAndWait(pcsName, "decode", 1, 6, 27, 0)

	tests.Logger.Info("Step 8: Verify PGM after decode scale-out")
	if err := waitForPGMComposition(tc, pcsName, 0, []pgComposition{
		pg(map[string]int32{"frontend": 1}, map[string]int32{"prefill": 2, "decode": 2}),
		pg(map[string]int32{"frontend": 2}, map[string]int32{"prefill": 2, "decode": 2}),
		pg(nil, map[string]int32{"prefill": 1}),
		pg(nil, map[string]int32{"prefill": 1}),
		pg(nil, map[string]int32{"decode": 1}),
		pg(nil, map[string]int32{"decode": 1}),
	}); err != nil {
		t.Fatalf("Step 8 PGM mismatch: %v", err)
	}

	// -----------------------------------------------------------------------
	// Steps 9–10: Coherent update of frontend, pworker, and dworker.
	// After update, all replicas reform into full MVU gangs:
	// Expected PGM: 3× {F:1,P:2,D:2}
	// -----------------------------------------------------------------------
	tests.Logger.Info("Step 9: Trigger coherent update of frontend, pworker, dworker")
	for _, clique := range []string{"frontend", "pworker", "dworker"} {
		if err := triggerPodCliqueUpdate(tc, clique); err != nil {
			t.Fatalf("Failed to trigger update on %s: %v", clique, err)
		}
	}
	if err := waitForCoherentUpdateComplete(tc, 1); err != nil {
		t.Fatalf("Step 9 coherent update did not complete: %v", err)
	}

	tests.Logger.Info("Step 10: Verify PGM — 3× {F:1,P:2,D:2}")
	if err := waitForPGMComposition(tc, pcsName, 0, []pgComposition{
		pg(map[string]int32{"frontend": 1}, map[string]int32{"prefill": 2, "decode": 2}),
		pg(map[string]int32{"frontend": 1}, map[string]int32{"prefill": 2, "decode": 2}),
		pg(map[string]int32{"frontend": 1}, map[string]int32{"prefill": 2, "decode": 2}),
	}); err != nil {
		t.Fatalf("Step 10 PGM mismatch: %v", err)
	}

	// -----------------------------------------------------------------------
	// Steps 11–12: Scale-in frontend 3 → 1. Total pods: 27 - 2 = 25.
	// Expected PGM: {F:1,P:2,D:2}, {P:2,D:2}, {P:2,D:2}
	// -----------------------------------------------------------------------
	tests.Logger.Info("Step 11: Scale-in frontend 3 → 1 (expect 25 pods)")
	if err := scalePodCliqueInPCS(tc, "frontend", 1); err != nil {
		t.Fatalf("Failed to scale frontend to 1: %v", err)
	}
	if err := tc.WaitForPods(25); err != nil {
		t.Fatalf("Step 11: did not reach 25 pods: %v", err)
	}

	tests.Logger.Info("Step 12: Verify PGM after frontend scale-in")
	if err := waitForPGMComposition(tc, pcsName, 0, []pgComposition{
		pg(map[string]int32{"frontend": 1}, map[string]int32{"prefill": 2, "decode": 2}),
		pg(nil, map[string]int32{"prefill": 2, "decode": 2}),
		pg(nil, map[string]int32{"prefill": 2, "decode": 2}),
	}); err != nil {
		t.Fatalf("Step 12 PGM mismatch: %v", err)
	}

	// -----------------------------------------------------------------------
	// Steps 13–14: Scale-in prefill 6 → 4. Total pods: 25 - 4 = 21.
	// Expected PGM: {F:1,P:2,D:2}, {P:2,D:2}, {D:2}
	// -----------------------------------------------------------------------
	tests.Logger.Info("Step 13: Scale-in prefill 6 → 4 (expect 21 pods)")
	tc.ScalePCSGAcrossAllReplicasAndWait(pcsName, "prefill", 1, 4, 21, 0)

	tests.Logger.Info("Step 14: Verify PGM after prefill scale-in to 4")
	if err := waitForPGMComposition(tc, pcsName, 0, []pgComposition{
		pg(map[string]int32{"frontend": 1}, map[string]int32{"prefill": 2, "decode": 2}),
		pg(nil, map[string]int32{"prefill": 2, "decode": 2}),
		pg(nil, map[string]int32{"decode": 2}),
	}); err != nil {
		t.Fatalf("Step 14 PGM mismatch: %v", err)
	}

	// -----------------------------------------------------------------------
	// Steps 15–16: Scale-in decode 6 → 4. Total pods: 21 - 4 = 17.
	// Expected PGM: {F:1,P:2,D:2}, {P:2,D:2}
	// -----------------------------------------------------------------------
	tests.Logger.Info("Step 15: Scale-in decode 6 → 4 (expect 17 pods)")
	tc.ScalePCSGAcrossAllReplicasAndWait(pcsName, "decode", 1, 4, 17, 0)

	tests.Logger.Info("Step 16: Verify PGM after decode scale-in to 4")
	if err := waitForPGMComposition(tc, pcsName, 0, []pgComposition{
		pg(map[string]int32{"frontend": 1}, map[string]int32{"prefill": 2, "decode": 2}),
		pg(nil, map[string]int32{"prefill": 2, "decode": 2}),
	}); err != nil {
		t.Fatalf("Step 16 PGM mismatch: %v", err)
	}

	// -----------------------------------------------------------------------
	// Steps 17–18: Scale-in prefill 4 → 2. Total pods: 17 - 4 = 13.
	// Expected PGM: {F:1,P:2,D:2}, {D:2}
	// -----------------------------------------------------------------------
	tests.Logger.Info("Step 17: Scale-in prefill 4 → 2 (expect 13 pods)")
	tc.ScalePCSGAcrossAllReplicasAndWait(pcsName, "prefill", 1, 2, 13, 0)

	tests.Logger.Info("Step 18: Verify PGM after prefill scale-in to 2")
	if err := waitForPGMComposition(tc, pcsName, 0, []pgComposition{
		pg(map[string]int32{"frontend": 1}, map[string]int32{"prefill": 2, "decode": 2}),
		pg(nil, map[string]int32{"decode": 2}),
	}); err != nil {
		t.Fatalf("Step 18 PGM mismatch: %v", err)
	}

	// -----------------------------------------------------------------------
	// Steps 19–20: Scale-in decode 4 → 2. Total pods: 13 - 4 = 9.
	// Expected PGM: {F:1,P:2,D:2}
	// -----------------------------------------------------------------------
	tests.Logger.Info("Step 19: Scale-in decode 4 → 2 (expect 9 pods)")
	tc.ScalePCSGAcrossAllReplicasAndWait(pcsName, "decode", 1, 2, 9, 0)

	tests.Logger.Info("Step 20: Verify PGM after decode scale-in to 2")
	if err := waitForPGMComposition(tc, pcsName, 0, []pgComposition{
		pg(map[string]int32{"frontend": 1}, map[string]int32{"prefill": 2, "decode": 2}),
	}); err != nil {
		t.Fatalf("Step 20 PGM mismatch: %v", err)
	}

	// -----------------------------------------------------------------------
	// Steps 21–22: Scale out frontend 1 → 3. Total pods: 9 + 2 = 11.
	// Expected PGM: {F:3,P:2,D:2}
	// -----------------------------------------------------------------------
	tests.Logger.Info("Step 21: Scale out frontend 1 → 3 (expect 11 pods)")
	if err := scalePodCliqueInPCS(tc, "frontend", 3); err != nil {
		t.Fatalf("Failed to scale frontend to 3: %v", err)
	}
	if err := tc.WaitForPods(11); err != nil {
		t.Fatalf("Step 21: did not reach 11 pods: %v", err)
	}

	tests.Logger.Info("Step 22: Verify PGM after frontend scale-out")
	if err := waitForPGMComposition(tc, pcsName, 0, []pgComposition{
		pg(map[string]int32{"frontend": 3}, map[string]int32{"prefill": 2, "decode": 2}),
	}); err != nil {
		t.Fatalf("Step 22 PGM mismatch: %v", err)
	}

	// -----------------------------------------------------------------------
	// Steps 23–24: Scale out prefill 2 → 5. Total pods: 11 + 6 = 17.
	// Expected PGM: {F:3,P:2,D:2}, {P:1}, {P:1}, {P:1}
	// -----------------------------------------------------------------------
	tests.Logger.Info("Step 23: Scale out prefill 2 → 5 (expect 17 pods)")
	tc.ScalePCSGAcrossAllReplicasAndWait(pcsName, "prefill", 1, 5, 17, 0)

	tests.Logger.Info("Step 24: Verify PGM after prefill scale-out to 5")
	if err := waitForPGMComposition(tc, pcsName, 0, []pgComposition{
		pg(map[string]int32{"frontend": 3}, map[string]int32{"prefill": 2, "decode": 2}),
		pg(nil, map[string]int32{"prefill": 1}),
		pg(nil, map[string]int32{"prefill": 1}),
		pg(nil, map[string]int32{"prefill": 1}),
	}); err != nil {
		t.Fatalf("Step 24 PGM mismatch: %v", err)
	}

	// -----------------------------------------------------------------------
	// Steps 25–26: Scale out decode 2 → 5. Total pods: 17 + 6 = 23.
	// Expected PGM: {F:3,P:2,D:2}, {P:1}×3, {D:1}×3
	// -----------------------------------------------------------------------
	tests.Logger.Info("Step 25: Scale out decode 2 → 5 (expect 23 pods)")
	tc.ScalePCSGAcrossAllReplicasAndWait(pcsName, "decode", 1, 5, 23, 0)

	tests.Logger.Info("Step 26: Verify PGM after decode scale-out to 5")
	if err := waitForPGMComposition(tc, pcsName, 0, []pgComposition{
		pg(map[string]int32{"frontend": 3}, map[string]int32{"prefill": 2, "decode": 2}),
		pg(nil, map[string]int32{"prefill": 1}),
		pg(nil, map[string]int32{"prefill": 1}),
		pg(nil, map[string]int32{"prefill": 1}),
		pg(nil, map[string]int32{"decode": 1}),
		pg(nil, map[string]int32{"decode": 1}),
		pg(nil, map[string]int32{"decode": 1}),
	}); err != nil {
		t.Fatalf("Step 26 PGM mismatch: %v", err)
	}

	// -----------------------------------------------------------------------
	// Steps 27–28: Coherent update of frontend only.
	// frontend leaves the MVU gang; each of the 3 frontend pods gets its own tail PG.
	// Expected PGM: {P:2,D:2}, {P:1}×3, {D:1}×3, {F:1}×3
	// -----------------------------------------------------------------------
	tests.Logger.Info("Step 27: Trigger coherent update of frontend")
	if err := triggerPodCliqueUpdate(tc, "frontend"); err != nil {
		t.Fatalf("Failed to trigger frontend update: %v", err)
	}
	if err := waitForCoherentUpdateComplete(tc, 1); err != nil {
		t.Fatalf("Step 27 coherent update did not complete: %v", err)
	}

	tests.Logger.Info("Step 28: Verify PGM after frontend update")
	if err := waitForPGMComposition(tc, pcsName, 0, []pgComposition{
		pg(nil, map[string]int32{"prefill": 2, "decode": 2}),
		pg(nil, map[string]int32{"prefill": 1}),
		pg(nil, map[string]int32{"prefill": 1}),
		pg(nil, map[string]int32{"prefill": 1}),
		pg(nil, map[string]int32{"decode": 1}),
		pg(nil, map[string]int32{"decode": 1}),
		pg(nil, map[string]int32{"decode": 1}),
		pg(map[string]int32{"frontend": 1}, nil),
		pg(map[string]int32{"frontend": 1}, nil),
		pg(map[string]int32{"frontend": 1}, nil),
	}); err != nil {
		t.Fatalf("Step 28 PGM mismatch: %v", err)
	}

	// -----------------------------------------------------------------------
	// Steps 29–30: Scale out prefill 5 → 6. Total pods: 23 + 2 = 25.
	// Expected PGM: same as step 28 plus one {P:1}
	// -----------------------------------------------------------------------
	tests.Logger.Info("Step 29: Scale out prefill 5 → 6 (expect 25 pods)")
	tc.ScalePCSGAcrossAllReplicasAndWait(pcsName, "prefill", 1, 6, 25, 0)

	tests.Logger.Info("Step 30: Verify PGM after prefill scale-out to 6")
	if err := waitForPGMComposition(tc, pcsName, 0, []pgComposition{
		pg(nil, map[string]int32{"prefill": 2, "decode": 2}),
		pg(nil, map[string]int32{"prefill": 1}),
		pg(nil, map[string]int32{"prefill": 1}),
		pg(nil, map[string]int32{"prefill": 1}),
		pg(nil, map[string]int32{"decode": 1}),
		pg(nil, map[string]int32{"decode": 1}),
		pg(nil, map[string]int32{"decode": 1}),
		pg(map[string]int32{"frontend": 1}, nil),
		pg(map[string]int32{"frontend": 1}, nil),
		pg(map[string]int32{"frontend": 1}, nil),
		pg(nil, map[string]int32{"prefill": 1}),
	}); err != nil {
		t.Fatalf("Step 30 PGM mismatch: %v", err)
	}

	// -----------------------------------------------------------------------
	// Steps 31–32: Scale out decode 5 → 6. Total pods: 25 + 2 = 27.
	// Expected PGM: same as step 30 plus one {D:1}
	// -----------------------------------------------------------------------
	tests.Logger.Info("Step 31: Scale out decode 5 → 6 (expect 27 pods)")
	tc.ScalePCSGAcrossAllReplicasAndWait(pcsName, "decode", 1, 6, 27, 0)

	tests.Logger.Info("Step 32: Verify PGM after decode scale-out to 6")
	if err := waitForPGMComposition(tc, pcsName, 0, []pgComposition{
		pg(nil, map[string]int32{"prefill": 2, "decode": 2}),
		pg(nil, map[string]int32{"prefill": 1}),
		pg(nil, map[string]int32{"prefill": 1}),
		pg(nil, map[string]int32{"prefill": 1}),
		pg(nil, map[string]int32{"decode": 1}),
		pg(nil, map[string]int32{"decode": 1}),
		pg(nil, map[string]int32{"decode": 1}),
		pg(map[string]int32{"frontend": 1}, nil),
		pg(map[string]int32{"frontend": 1}, nil),
		pg(map[string]int32{"frontend": 1}, nil),
		pg(nil, map[string]int32{"prefill": 1}),
		pg(nil, map[string]int32{"decode": 1}),
	}); err != nil {
		t.Fatalf("Step 32 PGM mismatch: %v", err)
	}

	// -----------------------------------------------------------------------
	// Steps 33–34: Coherent update of pleader and dleader.
	// prefill and decode tail PGs consolidate with the MVU gang.
	// Expected PGM: 3× {P:2,D:2}, 3× {F:1}
	// -----------------------------------------------------------------------
	tests.Logger.Info("Step 33: Trigger coherent update of pleader, dleader")
	for _, clique := range []string{"pleader", "dleader"} {
		if err := triggerPodCliqueUpdate(tc, clique); err != nil {
			t.Fatalf("Failed to trigger update on %s: %v", clique, err)
		}
	}
	if err := waitForCoherentUpdateComplete(tc, 1); err != nil {
		t.Fatalf("Step 33 coherent update did not complete: %v", err)
	}

	tests.Logger.Info("Step 34: Verify PGM — 3× {P:2,D:2}, 3× {F:1}")
	if err := waitForPGMComposition(tc, pcsName, 0, []pgComposition{
		pg(nil, map[string]int32{"prefill": 2, "decode": 2}),
		pg(nil, map[string]int32{"prefill": 2, "decode": 2}),
		pg(nil, map[string]int32{"prefill": 2, "decode": 2}),
		pg(map[string]int32{"frontend": 1}, nil),
		pg(map[string]int32{"frontend": 1}, nil),
		pg(map[string]int32{"frontend": 1}, nil),
	}); err != nil {
		t.Fatalf("Step 34 PGM mismatch: %v", err)
	}

	tests.Logger.Info("Test_CU1_MVUPodGangMapLifecycle completed successfully!")
}
