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

package utils

import "testing"

func TestMapContainsAll(t *testing.T) {
	tests := []struct {
		name     string
		target   map[string]string
		expected map[string]string
		want     bool
	}{
		{
			name: "exact match",
			target: map[string]string{
				"key": "value",
			},
			expected: map[string]string{
				"key": "value",
			},
			want: true,
		},
		{
			name: "target contains extra entries",
			target: map[string]string{
				"key":   "value",
				"extra": "preserved",
			},
			expected: map[string]string{
				"key": "value",
			},
			want: true,
		},
		{
			name: "target is missing an entry",
			target: map[string]string{
				"other": "value",
			},
			expected: map[string]string{
				"key": "value",
			},
		},
		{
			name: "target value differs",
			target: map[string]string{
				"key": "stale",
			},
			expected: map[string]string{
				"key": "value",
			},
		},
		{
			name:     "nil expected map",
			target:   nil,
			expected: nil,
			want:     true,
		},
		{
			name:     "non-nil expected map requires non-nil target",
			target:   nil,
			expected: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MapContainsAll(tt.target, tt.expected); got != tt.want {
				t.Fatalf("MapContainsAll() = %t, want %t", got, tt.want)
			}
		})
	}
}
