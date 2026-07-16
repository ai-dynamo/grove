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

package opts

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetPodCliqueDependencies tests parsing of podclique dependencies from various input formats.
func TestGetPodCliqueDependencies(t *testing.T) {
	tests := []struct {
		// Test case name for identifying failures
		name string
		// Input podCliques slice to test
		podCliques []string
		// Expected parsed dependencies map (podclique name -> min available count)
		expected map[string]int
		// Whether an error is expected
		expectError bool
		// Expected error message substring (only checked if expectError is true)
		errorContains string
	}{
		{
			// Basic valid input with multiple podcliques
			name:       "valid_multiple_podcliques",
			podCliques: []string{"podclique-a:3", "podclique-b:5"},
			expected: map[string]int{
				"podclique-a": 3,
				"podclique-b": 5,
			},
			expectError: false,
		},
		{
			// Single podclique with valid format
			name:       "valid_single_podclique",
			podCliques: []string{"my-podclique:1"},
			expected: map[string]int{
				"my-podclique": 1,
			},
			expectError: false,
		},
		{
			// Empty input should result in empty map
			name:        "empty_input",
			podCliques:  []string{},
			expected:    map[string]int{},
			expectError: false,
		},
		{
			// Input with whitespace should be trimmed and ignored if empty
			name:        "whitespace_only_entries",
			podCliques:  []string{"  ", "\t", ""},
			expected:    map[string]int{},
			expectError: false,
		},
		{
			// Valid input with extra whitespace around the entire entry should be trimmed
			name:       "input_with_whitespace",
			podCliques: []string{"  podclique-a:2  ", "podclique-b:4"},
			expected: map[string]int{
				"podclique-a": 2,
				"podclique-b": 4,
			},
			expectError: false,
		},
		{
			// Valid input with extra whitespace around the colon should be trimmed
			name:       "input_with_whitespace_around_colon",
			podCliques: []string{"  podclique-a :   2  ", "podclique-b:  4"},
			expected: map[string]int{
				"podclique-a": 2,
				"podclique-b": 4,
			},
			expectError: false,
		},
		{
			// Zero replicas should cause error
			name:          "zero_replicas",
			podCliques:    []string{"podclique-zero:0"},
			expected:      nil,
			expectError:   true,
			errorContains: "replica count must be positive, got 0",
		},
		{
			// Missing colon separator should cause error
			name:          "missing_colon",
			podCliques:    []string{"podclique-invalid"},
			expected:      nil,
			expectError:   true,
			errorContains: "expected two values per podclique, found 1",
		},
		{
			// Too many colon separators should cause error
			name:          "too_many_colons",
			podCliques:    []string{"podclique:extra:parts:3"},
			expected:      nil,
			expectError:   true,
			errorContains: "expected two values per podclique, found 4",
		},
		{
			// Non-numeric replica count should cause error
			name:          "non_numeric_replicas",
			podCliques:    []string{"podclique-a:invalid"},
			expected:      nil,
			expectError:   true,
			errorContains: "failed to convert replicas to int",
		},
		{
			// Negative replica count should cause error
			name:          "negative_replicas",
			podCliques:    []string{"podclique-negative:-1"},
			expected:      nil,
			expectError:   true,
			errorContains: "replica count must be positive, got -1",
		},
		{
			// Empty replica count should cause error
			name:          "empty_replicas",
			podCliques:    []string{"podclique-empty-replicas:"},
			expected:      nil,
			expectError:   true,
			errorContains: "failed to convert replicas to int",
		},
		{
			// Mixed valid and invalid entries - should fail on first invalid
			name:          "mixed_valid_invalid",
			podCliques:    []string{"podclique-a:3", "invalid-entry"},
			expected:      nil,
			expectError:   true,
			errorContains: "expected two values per podclique, found 1",
		},
		{
			// Empty podclique name should cause error
			name:          "empty_podclique_name",
			podCliques:    []string{":5"},
			expected:      nil,
			expectError:   true,
			errorContains: "podclique name cannot be empty",
		},
		{
			// Whitespace-only podclique name should cause error
			name:          "whitespace_only_podclique_name",
			podCliques:    []string{"  :3"},
			expected:      nil,
			expectError:   true,
			errorContains: "podclique name cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := &CLIOptions{
				podCliques: tt.podCliques,
			}

			result, err := options.GetPodCliqueDependencies()

			if tt.expectError {
				require.Error(t, err, "Expected error for test case %s", tt.name)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains, "Error message should contain expected substring")
				}
				assert.Nil(t, result, "Result should be nil when error occurs")
			} else {
				require.NoError(t, err, "Expected no error for test case %s", tt.name)
				assert.Equal(t, tt.expected, result, "Result should match expected dependencies")
			}
		})
	}
}

// TestInitializeCLIOptions verifies that CLI options are initialized correctly.
func TestInitializeCLIOptions(t *testing.T) {
	// Reset pflag.CommandLine to avoid interference from other tests
	pflag.CommandLine = pflag.NewFlagSet("test", pflag.ExitOnError)

	config, err := InitializeCLIOptions()

	require.NoError(t, err, "InitializeCLIOptions should not return an error")
	assert.Equal(t, 0, len(config.podCliques), "podCliques slice should be empty initially")
}
