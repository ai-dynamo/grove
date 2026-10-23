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
	// ExpectedReplicaIndices identifies the exact PCS replica indices whose managed resources should exist.
	// Its length equals the desired replica count in the PCS specification.
	ExpectedReplicaIndices []int
	// ScaleOutReplicaIndices identifies expected replica indices whose standalone PodCliques and
	// PodCliqueScalingGroups have not all been created.
	ScaleOutReplicaIndices []int
	// ScaleInReplicaIndices identifies replica indices selected for removal.
	// Components delete managed resources belonging to these indices.
	ScaleInReplicaIndices []int
}

// Result reports replica-level deletion work still pending after a component
// operator has synchronized its resources.
type Result struct {
	// ScaleInReplicaIndicesAwaitingResourceCleanup identifies scale-in replica indices for which
	// this component still observes managed resources, including resources with a deletion timestamp.
	// The PodCliqueSet reconciler keeps an index in ScalingProgress.ScaleInReplicaIndices until every
	// replica-scoped component stops reporting it here.
	ScaleInReplicaIndicesAwaitingResourceCleanup []int
}
