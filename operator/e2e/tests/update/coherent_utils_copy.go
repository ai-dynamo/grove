//go:build ignore

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
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// waitForCoherentUpdateComplete waits for the coherent update to complete.
// Uses a 5-minute timeout since coherent updates involve multiple iterations.
func waitForCoherentUpdateComplete(tc *testctx.TestContext, expectedReplicas int32) error {
	tcLong := *tc
	tcLong.Timeout = 5 * time.Minute
	return waitForRollingUpdateComplete(&tcLong, expectedReplicas)
}

// getPodGangMap fetches the PodGangMap for a PCS replica.
func getPodGangMap(tc *testctx.TestContext, pcsName string, replicaIndex int32) (*grovev1alpha1.PodGangMap, error) {
	var pgm grovev1alpha1.PodGangMap
	name := fmt.Sprintf("%s-%d", pcsName, replicaIndex)
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Name: name, Namespace: tc.Namespace}, &pgm); err != nil {
		return nil, err
	}
	return &pgm, nil
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
	return pgComposition{pclqs: e.PodCliques, pcsgs: e.PodCliqueScalingGroups}
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

// waitForPGMComposition polls until the PodGangMap for the given PCS replica
// contains a multiset of entry compositions matching expected, ignoring entry
// names and order.
func waitForPGMComposition(tc *testctx.TestContext, pcsName string, replicaIndex int32, expected []pgComposition) error {
	deadline := time.Now().Add(tc.Timeout)
	var lastActual []pgComposition
	for {
		pgm, err := getPodGangMap(tc, pcsName, replicaIndex)
		if err == nil {
			actual := make([]pgComposition, 0, len(pgm.Spec.Entries))
			for _, e := range pgm.Spec.Entries {
				actual = append(actual, compositionFromEntry(e))
			}
			lastActual = actual
			if sameMultiset(actual, expected) {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for PodGangMap composition.\nexpected: %s\nactual:   %s",
				formatCompositions(expected), formatCompositions(lastActual))
		}
		time.Sleep(tc.Interval)
	}
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
