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

package validation

import (
	"k8s.io/utils/strings/slices"
)

// PodCliqueDependencyGraph is the graph structure established from the dependencies between PodCliques.
type PodCliqueDependencyGraph struct {
	adjacency map[string][]string
}

// NewPodCliqueDependencyGraph returns an empty PodCliqueDependencyGraph.
func NewPodCliqueDependencyGraph() *PodCliqueDependencyGraph {
	return &PodCliqueDependencyGraph{
		adjacency: make(map[string][]string),
	}
}

// AddDependencies adds all the `to` PodCliques that depend on the  `from` PodClique.
func (g *PodCliqueDependencyGraph) AddDependencies(from string, to []string) {
	g.adjacency[from] = append(g.adjacency[from], to...)
}

// GetUnknownCliques returns cliques that are not present in the discoveredCliques slice.
func (g *PodCliqueDependencyGraph) GetUnknownCliques(discoveredCliques []string) []string {
	unknownCliques := make([]string, 0, len(discoveredCliques))
	for _, toCliques := range g.adjacency {
		for _, toClique := range toCliques {
			if !slices.Contains(discoveredCliques, toClique) {
				unknownCliques = append(unknownCliques, toClique)
			}
		}
	}
	return unknownCliques
}

// GetStronglyConnectedCliques returns all strongly connected cliques in the graph representing the PodClique dependencies.
// A strongly connected node is a subgraph in which every node is reachable from every other node.
// Uses Tarjan's algorithm as it efficiently uses DFS to find all strongly connected components a.k.a cycles in a graph.
// See https://en.wikipedia.org/wiki/Tarjan%27s_strongly_connected_components_algorithm
// NOTE: For the use case of this operator, we do not consider sub-graphs with single node as strongly connected components.
func (g *PodCliqueDependencyGraph) GetStronglyConnectedCliques() [][]string {
	numNodes := len(g.adjacency)
	index := 0
	indices := make(map[string]int, numNodes)
	lowLink := make(map[string]int, numNodes)
	onStack := make(map[string]bool, numNodes)
	stack := make([]string, 0, numNodes)
	var sccs [][]string

	var strongConnect func(c string)
	strongConnect = func(c string) {
		indices[c] = index
		lowLink[c] = index
		index++
		stack = append(stack, c)
		onStack[c] = true
		// iterate over the adjacency list of the current clique
		for _, d := range g.adjacency[c] {
			if _, found := indices[d]; !found {
				strongConnect(d)
				lowLink[c] = min(lowLink[c], lowLink[d])
			} else if onStack[d] {
				lowLink[c] = min(lowLink[c], indices[d])
			}
		}

		if lowLink[c] == indices[c] {
			var scc []string
			var sccElem string
			for {
				sccElem, stack = stack[len(stack)-1], stack[:len(stack)-1]
				onStack[sccElem] = false
				scc = append(scc, sccElem)
				if sccElem == c {
					break
				}
			}
			if len(scc) > 1 {
				sccs = append(sccs, scc)
			}
		}
	}

	for c := range g.adjacency {
		if _, found := indices[c]; !found {
			strongConnect(c)
		}
	}

	return sccs
}
