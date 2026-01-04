/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSupportedTopologyDomains(t *testing.T) {
	expectedDomains := []TopologyDomain{
		TopologyDomainRegion,
		TopologyDomainZone,
		TopologyDomainDataCenter,
		TopologyDomainBlock,
		TopologyDomainRack,
		TopologyDomainHost,
		TopologyDomainNuma,
	}

	domains := SupportedTopologyDomains()

	// Verify all expected domains are present and no duplicates
	seen := make(map[TopologyDomain]bool)
	for _, domain := range domains {
		assert.False(t, seen[domain], "domain %s should not be duplicated", domain)
		seen[domain] = true
	}
	// Verify all expected domains are included
	for _, expected := range expectedDomains {
		assert.True(t, seen[expected], "domain %s should be in the returned list", expected)
	}
}

func TestSortTopologyLevels(t *testing.T) {
	tests := []struct {
		name     string
		input    []TopologyLevel
		expected []TopologyLevel
	}{
		{
			name:     "empty slice",
			input:    []TopologyLevel{},
			expected: []TopologyLevel{},
		},
		{
			name: "single level",
			input: []TopologyLevel{
				{Domain: TopologyDomainZone, Key: "zone-key"},
			},
			expected: []TopologyLevel{
				{Domain: TopologyDomainZone, Key: "zone-key"},
			},
		},
		{
			name: "already sorted",
			input: []TopologyLevel{
				{Domain: TopologyDomainRegion, Key: "region-key"},
				{Domain: TopologyDomainZone, Key: "zone-key"},
				{Domain: TopologyDomainHost, Key: "host-key"},
			},
			expected: []TopologyLevel{
				{Domain: TopologyDomainRegion, Key: "region-key"},
				{Domain: TopologyDomainZone, Key: "zone-key"},
				{Domain: TopologyDomainHost, Key: "host-key"},
			},
		},
		{
			name: "reverse sorted",
			input: []TopologyLevel{
				{Domain: TopologyDomainHost, Key: "host-key"},
				{Domain: TopologyDomainZone, Key: "zone-key"},
				{Domain: TopologyDomainRegion, Key: "region-key"},
			},
			expected: []TopologyLevel{
				{Domain: TopologyDomainRegion, Key: "region-key"},
				{Domain: TopologyDomainZone, Key: "zone-key"},
				{Domain: TopologyDomainHost, Key: "host-key"},
			},
		},
		{
			name: "random order",
			input: []TopologyLevel{
				{Domain: TopologyDomainRack, Key: "rack-key"},
				{Domain: TopologyDomainRegion, Key: "region-key"},
				{Domain: TopologyDomainNuma, Key: "numa-key"},
				{Domain: TopologyDomainZone, Key: "zone-key"},
			},
			expected: []TopologyLevel{
				{Domain: TopologyDomainRegion, Key: "region-key"},
				{Domain: TopologyDomainZone, Key: "zone-key"},
				{Domain: TopologyDomainRack, Key: "rack-key"},
				{Domain: TopologyDomainNuma, Key: "numa-key"},
			},
		},
		{
			name: "all domains",
			input: []TopologyLevel{
				{Domain: TopologyDomainNuma, Key: "numa-key"},
				{Domain: TopologyDomainHost, Key: "host-key"},
				{Domain: TopologyDomainRack, Key: "rack-key"},
				{Domain: TopologyDomainBlock, Key: "block-key"},
				{Domain: TopologyDomainDataCenter, Key: "dc-key"},
				{Domain: TopologyDomainZone, Key: "zone-key"},
				{Domain: TopologyDomainRegion, Key: "region-key"},
			},
			expected: []TopologyLevel{
				{Domain: TopologyDomainRegion, Key: "region-key"},
				{Domain: TopologyDomainZone, Key: "zone-key"},
				{Domain: TopologyDomainDataCenter, Key: "dc-key"},
				{Domain: TopologyDomainBlock, Key: "block-key"},
				{Domain: TopologyDomainRack, Key: "rack-key"},
				{Domain: TopologyDomainHost, Key: "host-key"},
				{Domain: TopologyDomainNuma, Key: "numa-key"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make a copy to avoid modifying test data
			input := make([]TopologyLevel, len(tt.input))
			copy(input, tt.input)

			// Sort the input
			SortTopologyLevels(input)

			// Check if sorted correctly
			if len(input) != len(tt.expected) {
				t.Fatalf("length mismatch: got %d, want %d", len(input), len(tt.expected))
			}

			for i := range input {
				assert.Equal(t, input[i].Domain, tt.expected[i].Domain, "at index %d: got domain %v, want %v", i, input[i].Domain, tt.expected[i].Domain)
				assert.Equal(t, input[i].Key, tt.expected[i].Key, "at index %d: got key %v, want %v", i, input[i].Key, tt.expected[i].Key)
			}
		})
	}
}
