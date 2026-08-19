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
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetUnknownCliques(t *testing.T) {
	testCases := []struct {
		name              string
		discoveredCliques []string
		cliqueDeps        map[string][]string
		unknownCliques    []string
	}{
		{
			name:              "All clique dependencies have been defined in the PodCliqueSet",
			discoveredCliques: []string{"c1", "c2", "c3"},
			cliqueDeps: map[string][]string{
				"c2": {"c1"},
				"c3": {"c1"},
			},
			unknownCliques: []string{},
		},
		{
			name:              "Some clique dependencies have not been defined in the PodCliqueSet",
			discoveredCliques: []string{"c1", "c2", "c3"},
			cliqueDeps: map[string][]string{
				"c2": {"c1"},
				"c3": {"c1", "c4"},
			},
			unknownCliques: []string{"c4"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			depG := NewPodCliqueDependencyGraph()
			for c, deps := range tc.cliqueDeps {
				depG.AddDependencies(c, deps)
			}
			actualUnknownCliques := depG.GetUnknownCliques(tc.discoveredCliques)
			assert.ElementsMatch(t, tc.unknownCliques, actualUnknownCliques)
		})
	}
}

func TestGetStronglyConnectedCliques(t *testing.T) {
	testCases := []struct {
		name                             string
		cliqueDeps                       map[string][]string
		expectedStronglyConnectedCliques [][]string
	}{
		{
			name: "No strongly connected cliques",
			cliqueDeps: map[string][]string{
				"c3": {"c1"},
				"c4": {"c1"},
				"c5": {"c3", "c4"},
				"c2": {},
			},
			expectedStronglyConnectedCliques: [][]string{},
		},
		{
			name: "One cycle of strongly connected cliques",
			cliqueDeps: map[string][]string{
				"c1": {"c2"},
				"c2": {"c3", "c4"},
				"c3": {"c1"},
				"c4": {"c5"},
			},
			expectedStronglyConnectedCliques: [][]string{
				{"c1", "c2", "c3"},
			},
		},
		{
			name: "More than one cycle of strongly connected cliques",
			cliqueDeps: map[string][]string{
				"c1": {"c2"},
				"c2": {"c3"},
				"c3": {"c1", "c4"},
				"c4": {"c5"},
				"c6": {"c5", "c8"},
				"c7": {"c6"},
				"c8": {"c7", "c5"},
				"c9": {"c8", "c7"},
			},
			expectedStronglyConnectedCliques: [][]string{
				{"c1", "c2", "c3"},
				{"c6", "c7", "c8"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			depG := NewPodCliqueDependencyGraph()
			for c, deps := range tc.cliqueDeps {
				depG.AddDependencies(c, deps)
			}
			actualStronglyConnectedCliques := depG.GetStronglyConnectedCliques()
			// sort the expected strongly connected cliques
			for _, expectedScc := range tc.expectedStronglyConnectedCliques {
				slices.Sort(expectedScc)
			}
			// sort the actual strongly connected cliques
			for _, actualScc := range actualStronglyConnectedCliques {
				slices.Sort(actualScc)
			}
			assert.ElementsMatch(t, tc.expectedStronglyConnectedCliques, actualStronglyConnectedCliques)
		})
	}
}
