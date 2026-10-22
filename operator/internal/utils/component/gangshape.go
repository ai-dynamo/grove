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

package component

import (
	"fmt"
	"hash/fnv"
	"slices"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	"k8s.io/apimachinery/pkg/util/rand"
)

// ComputeGangShapeHash returns the gang-shape hash of the PodCliqueSet: a hash of the init.minReplicas of every
// clique template and every PodCliqueScalingGroup config, keyed by name. init.minReplicas fixes the gang
// shape of a replica (PodGang minimums, the PodGangMap base layout and the init-container wait counts), so a
// change recreates the replicas. Nothing else enters the hash.
func ComputeGangShapeHash(pcs *grovecorev1beta1.PodCliqueSet) string {
	entries := make([]string, 0, len(pcs.Spec.Template.Cliques)+len(pcs.Spec.Template.PodCliqueScalingGroupConfigs))
	for _, cliqueTemplate := range pcs.Spec.Template.Cliques {
		entries = append(entries, fmt.Sprintf("clique/%s=%d", cliqueTemplate.Name, PCLQInitMinReplicas(&cliqueTemplate.Spec)))
	}
	for i := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		cfg := &pcs.Spec.Template.PodCliqueScalingGroupConfigs[i]
		entries = append(entries, fmt.Sprintf("pcsg/%s=%d", cfg.Name, PCSGConfigInitMinReplicas(cfg)))
	}
	slices.Sort(entries)
	hasher := fnv.New64a()
	for _, entry := range entries {
		_, _ = fmt.Fprintln(hasher, entry)
	}
	return rand.SafeEncodeString(fmt.Sprint(hasher.Sum64()))
}

// GangShapeHashLabelValue returns the gang-shape hash label to write on a PodClique or PodCliqueScalingGroup of
// the PodCliqueSet. A new child gets the current hash. An existing child keeps its value, so a stale replica
// stays detectable until it is recreated, and a child without the label adopts the current hash, so children
// created before the label existed are never recreated for it.
func GangShapeHashLabelValue(pcs *grovecorev1beta1.PodCliqueSet, existingLabels map[string]string) string {
	if existing, ok := existingLabels[apicommon.LabelGangShapeHash]; ok && existing != "" {
		return existing
	}
	return ComputeGangShapeHash(pcs)
}
