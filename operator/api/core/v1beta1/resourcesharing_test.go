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

package v1beta1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPCSResourceSharingFilterMatches(t *testing.T) {
	tests := []struct {
		name       string
		filter     *PCSResourceSharingFilter
		matchNames []string
		want       bool
	}{
		{
			name:       "nil filter broadcasts",
			matchNames: []string{"trainer", "batch"},
			want:       true,
		},
		{
			name:   "no names broadcasts",
			filter: &PCSResourceSharingFilter{ChildCliqueNames: []string{"batch"}},
			want:   true,
		},
		{
			name:       "standalone clique matches clique filter",
			filter:     &PCSResourceSharingFilter{ChildCliqueNames: []string{"batch"}},
			matchNames: []string{"batch"},
			want:       true,
		},
		{
			name:       "group matches scaling group filter",
			filter:     &PCSResourceSharingFilter{ChildScalingGroupNames: []string{"batch"}},
			matchNames: []string{"trainer", "batch"},
			want:       true,
		},
		{
			name:       "clique name does not match scaling group filter",
			filter:     &PCSResourceSharingFilter{ChildScalingGroupNames: []string{"batch"}},
			matchNames: []string{"batch"},
		},
		{
			name:       "group name does not match clique filter",
			filter:     &PCSResourceSharingFilter{ChildCliqueNames: []string{"batch"}},
			matchNames: []string{"trainer", "batch"},
		},
		{
			name:       "member clique name does not match another group",
			filter:     &PCSResourceSharingFilter{ChildScalingGroupNames: []string{"trainer"}},
			matchNames: []string{"trainer", "batch"},
		},
		{
			name:       "clique filter matches with a group present",
			filter:     &PCSResourceSharingFilter{ChildCliqueNames: []string{"trainer"}},
			matchNames: []string{"trainer", "batch"},
			want:       true,
		},
		{
			name: "either filter can match",
			filter: &PCSResourceSharingFilter{
				ChildCliqueNames:       []string{"batch"},
				ChildScalingGroupNames: []string{"batch"},
			},
			matchNames: []string{"trainer", "batch"},
			want:       true,
		},
		{
			name:       "unmatched names",
			filter:     &PCSResourceSharingFilter{ChildCliqueNames: []string{"batch"}},
			matchNames: []string{"worker", "other"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := PCSResourceSharingSpec{Filter: tt.filter}
			assert.Equal(t, tt.want, spec.FilterMatches(tt.matchNames...))
		})
	}
}
