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

// Package sync defines the data exchanged between the PodCliqueSet reconciler
// and its component operators during one reconciliation.
package sync

// State contains PodCliqueSet component input derived once for the current reconciliation.
// Replica index slices are sorted, contain no duplicates, and must not be modified by
// component operators.
type State struct {
	// ExpectedReplicaIndices identifies the exact PCS replica indices whose managed resources should exist
	// in this reconciliation. It is not the committed replica count. A same-index replacement target is
	// temporarily excluded while its source cleanup is pending.
	ExpectedReplicaIndices []int
	// ScaleOutReplicaIndices identifies expected PCS replica indices being provisioned or recreated.
	// Components may create the managed standalone PodCliques and PodCliqueScalingGroups at these indices.
	ScaleOutReplicaIndices []int
	// ScaleInReplicaIndices identifies PCS replica indices whose managed resources must be removed.
	// It includes ordinary scale-in and replacement-owned cleanup, including retired sources. These
	// reconcile-local cleanup indices are not necessarily recorded in PCS.Status.ScalingProgress.
	ScaleInReplicaIndices []int
}

// Result reports, per replica-scoped component, what the component observed for PCS replica cleanup after it
// synchronized its resources.
type Result struct {
	// NotApplicable indicates that the component manages no resources for this PodCliqueSet, because the feature
	// it manages is not configured for this PodCliqueSet. Such a component is excluded from per-replica cleanup
	// coverage, so it neither confirms nor blocks the release of a scale-in replica index.
	NotApplicable bool
	// CleanedUpReplicaIndices lists the scale-in PCS replica indices from State.ScaleInReplicaIndices for which
	// this component observes none of its managed replica-scoped resources. It is populated only when the
	// component did work, that is when NotApplicable is false.
	CleanedUpReplicaIndices []int
}
