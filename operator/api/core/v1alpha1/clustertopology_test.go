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

func TestIsTopologyDomainNarrower(t *testing.T) {
	tests := []struct {
		name     string
		domain   TopologyDomain
		other    TopologyDomain
		expected bool
	}{
		// Same domain comparisons - should all return false
		{
			name:     "region compared to region",
			domain:   TopologyDomainRegion,
			other:    TopologyDomainRegion,
			expected: false,
		},
		{
			name:     "zone compared to zone",
			domain:   TopologyDomainZone,
			other:    TopologyDomainZone,
			expected: false,
		},
		{
			name:     "datacenter compared to datacenter",
			domain:   TopologyDomainDataCenter,
			other:    TopologyDomainDataCenter,
			expected: false,
		},
		{
			name:     "block compared to block",
			domain:   TopologyDomainBlock,
			other:    TopologyDomainBlock,
			expected: false,
		},
		{
			name:     "rack compared to rack",
			domain:   TopologyDomainRack,
			other:    TopologyDomainRack,
			expected: false,
		},
		{
			name:     "host compared to host",
			domain:   TopologyDomainHost,
			other:    TopologyDomainHost,
			expected: false,
		},
		{
			name:     "numa compared to numa",
			domain:   TopologyDomainNuma,
			other:    TopologyDomainNuma,
			expected: false,
		},
		// Narrower domain comparisons - should return true
		{
			name:     "zone is narrower than region",
			domain:   TopologyDomainZone,
			other:    TopologyDomainRegion,
			expected: true,
		},
		{
			name:     "datacenter is narrower than zone",
			domain:   TopologyDomainDataCenter,
			other:    TopologyDomainZone,
			expected: true,
		},
		{
			name:     "block is narrower than datacenter",
			domain:   TopologyDomainBlock,
			other:    TopologyDomainDataCenter,
			expected: true,
		},
		{
			name:     "rack is narrower than block",
			domain:   TopologyDomainRack,
			other:    TopologyDomainBlock,
			expected: true,
		},
		{
			name:     "host is narrower than rack",
			domain:   TopologyDomainHost,
			other:    TopologyDomainRack,
			expected: true,
		},
		{
			name:     "numa is narrower than host",
			domain:   TopologyDomainNuma,
			other:    TopologyDomainHost,
			expected: true,
		},
		{
			name:     "numa is narrower than region (broadest)",
			domain:   TopologyDomainNuma,
			other:    TopologyDomainRegion,
			expected: true,
		},
		{
			name:     "host is narrower than region",
			domain:   TopologyDomainHost,
			other:    TopologyDomainRegion,
			expected: true,
		},
		{
			name:     "rack is narrower than region",
			domain:   TopologyDomainRack,
			other:    TopologyDomainRegion,
			expected: true,
		},
		{
			name:     "numa is narrower than zone",
			domain:   TopologyDomainNuma,
			other:    TopologyDomainZone,
			expected: true,
		},
		{
			name:     "host is narrower than datacenter",
			domain:   TopologyDomainHost,
			other:    TopologyDomainDataCenter,
			expected: true,
		},

		// Broader domain comparisons - should return false
		{
			name:     "region is not narrower than zone",
			domain:   TopologyDomainRegion,
			other:    TopologyDomainZone,
			expected: false,
		},
		{
			name:     "zone is not narrower than datacenter",
			domain:   TopologyDomainZone,
			other:    TopologyDomainDataCenter,
			expected: false,
		},
		{
			name:     "datacenter is not narrower than block",
			domain:   TopologyDomainDataCenter,
			other:    TopologyDomainBlock,
			expected: false,
		},
		{
			name:     "block is not narrower than rack",
			domain:   TopologyDomainBlock,
			other:    TopologyDomainRack,
			expected: false,
		},
		{
			name:     "rack is not narrower than host",
			domain:   TopologyDomainRack,
			other:    TopologyDomainHost,
			expected: false,
		},
		{
			name:     "host is not narrower than numa",
			domain:   TopologyDomainHost,
			other:    TopologyDomainNuma,
			expected: false,
		},
		{
			name:     "region is not narrower than numa (narrowest)",
			domain:   TopologyDomainRegion,
			other:    TopologyDomainNuma,
			expected: false,
		},
		{
			name:     "region is not narrower than rack",
			domain:   TopologyDomainRegion,
			other:    TopologyDomainRack,
			expected: false,
		},
		{
			name:     "zone is not narrower than numa",
			domain:   TopologyDomainZone,
			other:    TopologyDomainNuma,
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.domain.IsTopologyDomainNarrower(tc.other)
			assert.Equal(t, tc.expected, result,
				"IsTopologyDomainNarrower(%s, %s) = %v, want %v",
				tc.domain, tc.other, result, tc.expected)
		})
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

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Make a copy to avoid modifying test data
			input := make([]TopologyLevel, len(tc.input))
			copy(input, tc.input)

			// Sort the input
			SortTopologyLevels(input)

			// Check if sorted correctly
			if len(input) != len(tc.expected) {
				t.Fatalf("length mismatch: got %d, want %d", len(input), len(tc.expected))
			}

			for i := range input {
				assert.Equal(t, input[i].Domain, tc.expected[i].Domain, "at index %d: got domain %v, want %v", i, input[i].Domain, tc.expected[i].Domain)
				assert.Equal(t, input[i].Key, tc.expected[i].Key, "at index %d: got key %v, want %v", i, input[i].Key, tc.expected[i].Key)
			}
		})
	}
}
