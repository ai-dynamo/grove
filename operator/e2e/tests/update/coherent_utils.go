//go:build e2e

// /*
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
// */

package update

import (
	"fmt"
	"sort"
	"strings"
	"time"

	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/k8sclient"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// waitForCoherentUpdateComplete waits for the coherent update to complete.
// Uses a 5-minute timeout since coherent updates involve multiple iterations.
func waitForCoherentUpdateComplete(tc *testctx.TestContext, expectedReplicas int32) error {
	tcLong := *tc
	tcLong.Timeout = 5 * time.Minute
	return waitForRollingUpdateComplete(&tcLong, expectedReplicas)
}

// waitForPGMComposition polls until the PodGangMap for the given PCS replica
// contains a multiset of entry compositions matching expected, ignoring entry
// names and order.
func waitForPGMComposition(tc *testctx.TestContext, pcsName string, replicaIndex int32, expected []pgComposition) error {
	name := fmt.Sprintf("%s-%d", pcsName, replicaIndex)
	fetchPGM := waiter.FetchByName(name, k8sclient.Getter[*grovev1alpha1.PodGangMap](tc.Client, tc.Namespace))

	var lastActual []pgComposition
	predicate := waiter.Predicate[*grovev1alpha1.PodGangMap](func(pgm *grovev1alpha1.PodGangMap) bool {
		if pgm == nil {
			lastActual = nil
			return false
		}
		actual := make([]pgComposition, 0, len(pgm.Spec.Entries))
		for _, e := range pgm.Spec.Entries {
			actual = append(actual, compositionFromEntry(e))
		}
		lastActual = actual
		return sameMultiset(actual, expected)
	})

	w := waiter.New[*grovev1alpha1.PodGangMap]().
		WithTimeout(tc.Timeout).
		WithInterval(tc.Interval).
		WithRetryOnError()
	if err := w.WaitUntil(tc.Ctx, fetchPGM, predicate); err != nil {
		return fmt.Errorf("timed out waiting for PodGangMap composition.\nexpected: %s\nactual:   %s",
			formatCompositions(expected), formatCompositions(lastActual))
	}
	return nil
}

// pgComposition captures the composition of a single PodGangMap entry for
// equality checks that ignore entry name and ordering.
type pgComposition struct {
	pclqs map[string]int32
	pcsgs map[string]int32
}

func pg(pclqs map[string]int32, pcsgs map[string]int32) pgComposition {
	return pgComposition{pclqs: pclqs, pcsgs: pcsgs}
}

func compositionFromEntry(e grovev1alpha1.PodGangEntry) pgComposition {
	var pcsgs map[string]int32
	if len(e.PCSGReplicaIndices) > 0 {
		pcsgs = make(map[string]int32, len(e.PCSGReplicaIndices))
		for name, indices := range e.PCSGReplicaIndices {
			pcsgs[name] = int32(len(indices))
		}
	}
	return pgComposition{pclqs: e.PodCliques, pcsgs: pcsgs}
}

func canonicalKey(c pgComposition) string {
	var b strings.Builder
	b.WriteString("pclqs{")
	b.WriteString(sortedMapStr(c.pclqs))
	b.WriteString("}|pcsgs{")
	b.WriteString(sortedMapStr(c.pcsgs))
	b.WriteString("}")
	return b.String()
}

func sortedMapStr(m map[string]int32) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, ",")
}

func multisetCounts(items []pgComposition) map[string]int {
	counts := make(map[string]int, len(items))
	for _, c := range items {
		counts[canonicalKey(c)]++
	}
	return counts
}

func sameMultiset(a, b []pgComposition) bool {
	if len(a) != len(b) {
		return false
	}
	ca, cb := multisetCounts(a), multisetCounts(b)
	if len(ca) != len(cb) {
		return false
	}
	for k, v := range ca {
		if cb[k] != v {
			return false
		}
	}
	return true
}

func formatCompositions(items []pgComposition) string {
	parts := make([]string, 0, len(items))
	for _, c := range items {
		parts = append(parts, "{"+canonicalKey(c)+"}")
	}
	sort.Strings(parts)
	return "[" + strings.Join(parts, ", ") + "]"
}

// deleteFirstPodOfClique deletes the first pod of the given clique.
func deleteFirstPodOfClique(tc *testctx.TestContext, cliqueName string) error {
	podName, err := getFirstPodForClique(tc, cliqueName)
	if err != nil {
		return err
	}
	return tc.Client.Delete(tc.Ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: tc.Namespace}})
}

// deleteAllPodsOfClique deletes every pod belonging to the named clique.
func deleteAllPodsOfClique(tc *testctx.TestContext, cliqueName string) error {
	podNames, err := getPodsForClique(tc, cliqueName)
	if err != nil {
		return err
	}
	for _, name := range podNames {
		if err := tc.Client.Delete(tc.Ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: tc.Namespace}}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete pod %s: %w", name, err)
		}
	}
	return nil
}

// -----------------------------------------------------------------------
// Starting-state helpers for the CU1 test series.
//
// Each helper drives the cluster from the fresh-deploy state (21 pods) to the
// named end state so that individual Test_CU1_* tests can reach their own
// starting point independently, without depending on prior tests having run.
// -----------------------------------------------------------------------

// mvu1PGMAfterDeploy is the expected PGM immediately after deploying workload-mvu-test1.
var mvu1PGMAfterDeploy = []pgComposition{
	pg(map[string]int32{"frontend": 1}, map[string]int32{"prefill": 2, "decode": 2}),
	pg(map[string]int32{"frontend": 2}, map[string]int32{"prefill": 2, "decode": 2}),
	pg(nil, map[string]int32{"decode": 1}),
}

// mvu1PGMAfterScaleOut is the expected PGM after prefill 4→6 and decode 5→6.
var mvu1PGMAfterScaleOut = []pgComposition{
	pg(map[string]int32{"frontend": 1}, map[string]int32{"prefill": 2, "decode": 2}),
	pg(map[string]int32{"frontend": 2}, map[string]int32{"prefill": 2, "decode": 2}),
	pg(nil, map[string]int32{"prefill": 1}),
	pg(nil, map[string]int32{"prefill": 1}),
	pg(nil, map[string]int32{"decode": 1}),
	pg(nil, map[string]int32{"decode": 1}),
}

// mvu1PGMAfterCoherentUpdates is the expected PGM after both full-clique coherent updates.
var mvu1PGMAfterCoherentUpdates = []pgComposition{
	pg(map[string]int32{"frontend": 1}, map[string]int32{"prefill": 2, "decode": 2}),
	pg(map[string]int32{"frontend": 1}, map[string]int32{"prefill": 2, "decode": 2}),
	pg(map[string]int32{"frontend": 1}, map[string]int32{"prefill": 2, "decode": 2}),
}

// mvu1PGMAfterScaleIn is the expected PGM after cascading scale-in to minimum counts.
var mvu1PGMAfterScaleIn = []pgComposition{
	pg(map[string]int32{"frontend": 1}, map[string]int32{"prefill": 2, "decode": 2}),
}

// mvu1PGMAfterSecondScaleOut is the expected PGM after the second scale-out
// (frontend 1→3, prefill 2→5, decode 2→5).
var mvu1PGMAfterSecondScaleOut = []pgComposition{
	pg(map[string]int32{"frontend": 3}, map[string]int32{"prefill": 2, "decode": 2}),
	pg(nil, map[string]int32{"prefill": 1}),
	pg(nil, map[string]int32{"prefill": 1}),
	pg(nil, map[string]int32{"prefill": 1}),
	pg(nil, map[string]int32{"decode": 1}),
	pg(nil, map[string]int32{"decode": 1}),
	pg(nil, map[string]int32{"decode": 1}),
}

// mvu1PGMAfterFrontendUpdate is the expected PGM after the frontend-only coherent update
// with prefill/decode scaled out to 6 each.
var mvu1PGMAfterFrontendUpdate = []pgComposition{
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
}

// mvu1PGMAfterLeaderUpdate is the expected PGM after the pleader/dleader coherent update.
var mvu1PGMAfterLeaderUpdate = []pgComposition{
	pg(nil, map[string]int32{"prefill": 2, "decode": 2}),
	pg(nil, map[string]int32{"prefill": 2, "decode": 2}),
	pg(nil, map[string]int32{"prefill": 2, "decode": 2}),
	pg(map[string]int32{"frontend": 1}, nil),
	pg(map[string]int32{"frontend": 1}, nil),
	pg(map[string]int32{"frontend": 1}, nil),
}

// reachMVU1StateAfterScaleOut drives the cluster from the fresh-deploy state (21 pods)
// to the state after prefill 4→6 and decode 5→6 (27 pods).
func reachMVU1StateAfterScaleOut(tc *testctx.TestContext, pcsName string) error {
	tc.ScalePCSGAcrossAllReplicasAndWait(pcsName, "prefill", 1, 6, 25, 0)
	tc.ScalePCSGAcrossAllReplicasAndWait(pcsName, "decode", 1, 6, 27, 0)
	return waitForPGMComposition(tc, pcsName, 0, mvu1PGMAfterScaleOut)
}

// reachMVU1StateAfterCoherentUpdates drives the cluster from the scale-out state (27 pods)
// to the state after both full-clique coherent updates (3× {F:1,P:2,D:2}).
func reachMVU1StateAfterCoherentUpdates(tc *testctx.TestContext, pcsName string) error {
	for _, clique := range []string{"frontend", "pleader", "dleader"} {
		if err := triggerPodCliqueUpdate(tc, clique); err != nil {
			return fmt.Errorf("trigger update %s: %w", clique, err)
		}
	}
	if err := waitForCoherentUpdateComplete(tc, 1); err != nil {
		return fmt.Errorf("first coherent update: %w", err)
	}
	for _, clique := range []string{"frontend", "pworker", "dworker"} {
		if err := triggerPodCliqueUpdate(tc, clique); err != nil {
			return fmt.Errorf("trigger update %s: %w", clique, err)
		}
	}
	if err := waitForCoherentUpdateComplete(tc, 1); err != nil {
		return fmt.Errorf("second coherent update: %w", err)
	}
	return waitForPGMComposition(tc, pcsName, 0, mvu1PGMAfterCoherentUpdates)
}

// reachMVU1StateAfterScaleIn drives the cluster from the post-updates state (27 pods)
// to the minimal state (9 pods, prefill=2, decode=2, frontend=1).
func reachMVU1StateAfterScaleIn(tc *testctx.TestContext, pcsName string) error {
	if err := scalePodCliqueInPCS(tc, "frontend", 1); err != nil {
		return fmt.Errorf("scale frontend: %w", err)
	}
	if err := tc.WaitForPods(25); err != nil {
		return fmt.Errorf("wait 25 pods: %w", err)
	}
	tc.ScalePCSGAcrossAllReplicasAndWait(pcsName, "prefill", 1, 4, 21, 0)
	tc.ScalePCSGAcrossAllReplicasAndWait(pcsName, "decode", 1, 4, 17, 0)
	tc.ScalePCSGAcrossAllReplicasAndWait(pcsName, "prefill", 1, 2, 13, 0)
	tc.ScalePCSGAcrossAllReplicasAndWait(pcsName, "decode", 1, 2, 9, 0)
	return waitForPGMComposition(tc, pcsName, 0, mvu1PGMAfterScaleIn)
}

// reachMVU1StateAfterSecondScaleOut drives the cluster from the minimal state (9 pods)
// to the second scale-out state (23 pods, frontend=3, prefill=5, decode=5).
func reachMVU1StateAfterSecondScaleOut(tc *testctx.TestContext, pcsName string) error {
	if err := scalePodCliqueInPCS(tc, "frontend", 3); err != nil {
		return fmt.Errorf("scale frontend: %w", err)
	}
	if err := tc.WaitForPods(11); err != nil {
		return fmt.Errorf("wait 11 pods: %w", err)
	}
	tc.ScalePCSGAcrossAllReplicasAndWait(pcsName, "prefill", 1, 5, 17, 0)
	tc.ScalePCSGAcrossAllReplicasAndWait(pcsName, "decode", 1, 5, 23, 0)
	return waitForPGMComposition(tc, pcsName, 0, mvu1PGMAfterSecondScaleOut)
}

// reachMVU1StateAfterFrontendUpdate drives the cluster from the second scale-out state
// (23 pods) to the state after the frontend-only coherent update plus prefill/decode
// scale-out to 6 each (27 pods).
func reachMVU1StateAfterFrontendUpdate(tc *testctx.TestContext, pcsName string) error {
	if err := triggerPodCliqueUpdate(tc, "frontend"); err != nil {
		return fmt.Errorf("trigger frontend update: %w", err)
	}
	if err := waitForCoherentUpdateComplete(tc, 1); err != nil {
		return fmt.Errorf("frontend coherent update: %w", err)
	}
	tc.ScalePCSGAcrossAllReplicasAndWait(pcsName, "prefill", 1, 6, 25, 0)
	tc.ScalePCSGAcrossAllReplicasAndWait(pcsName, "decode", 1, 6, 27, 0)
	return waitForPGMComposition(tc, pcsName, 0, mvu1PGMAfterFrontendUpdate)
}

// reachMVU1StateAfterLeaderUpdate drives the cluster from the frontend-update state
// (27 pods) to the state after the pleader/dleader coherent update.
func reachMVU1StateAfterLeaderUpdate(tc *testctx.TestContext, pcsName string) error {
	for _, clique := range []string{"pleader", "dleader"} {
		if err := triggerPodCliqueUpdate(tc, clique); err != nil {
			return fmt.Errorf("trigger update %s: %w", clique, err)
		}
	}
	if err := waitForCoherentUpdateComplete(tc, 1); err != nil {
		return fmt.Errorf("leader coherent update: %w", err)
	}
	return waitForPGMComposition(tc, pcsName, 0, mvu1PGMAfterLeaderUpdate)
}

// waitForGangTerminationAndRecreation polls until the workload's pod count drops
// below expectedAfter (gang terminated) and then climbs back to expectedAfter
// (recreation). Nodes cordoned during termination keep pods pending until the
// test uncordons them, so the recreation half waits up to tc.Timeout.
func waitForGangTerminationAndRecreation(tc *testctx.TestContext, expectedAfter int) error {
	deadline := time.Now().Add(tc.Timeout)

	dropped := false
	for !dropped {
		pods, err := tc.ListPods()
		if err == nil && len(pods.Items) < expectedAfter {
			dropped = true
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for gang-termination to drop pod count below %d", expectedAfter)
		}
		time.Sleep(tc.Interval)
	}

	if err := tc.WaitForPods(expectedAfter); err != nil {
		return fmt.Errorf("gang-recreation did not reach %d pods: %w", expectedAfter, err)
	}
	return nil
}
