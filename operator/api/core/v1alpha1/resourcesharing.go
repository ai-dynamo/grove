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

package v1alpha1

import "slices"

// GetBase returns the base resource sharing fields.
func (b *ResourceSharingSpec) GetBase() *ResourceSharingSpec { return b }

// FilterMatches returns true since PCLQ-level entries have no filter.
func (b *ResourceSharingSpec) FilterMatches(_ ...string) bool { return true }

// GetBase returns the base resource sharing fields.
func (s *PCSResourceSharingSpec) GetBase() *ResourceSharingSpec {
	return &s.ResourceSharingSpec
}

// FilterMatches checks whether the PCS filter allows injection for the given names.
// When Filter is nil or no matchNames are provided, all children match (broadcast).
func (s *PCSResourceSharingSpec) FilterMatches(matchNames ...string) bool {
	if s.Filter == nil || len(matchNames) == 0 {
		return true
	}
	for _, name := range matchNames {
		if slices.Contains(s.Filter.ChildCliqueNames, name) || slices.Contains(s.Filter.ChildScalingGroupNames, name) {
			return true
		}
	}
	return false
}

// GetBase returns the base resource sharing fields.
func (s *PCSGResourceSharingSpec) GetBase() *ResourceSharingSpec {
	return &s.ResourceSharingSpec
}

// FilterMatches checks whether the PCSG filter allows injection for the given names.
// When Filter is nil or no matchNames are provided, all children match (broadcast).
func (s *PCSGResourceSharingSpec) FilterMatches(matchNames ...string) bool {
	if s.Filter == nil || len(matchNames) == 0 {
		return true
	}
	for _, name := range matchNames {
		if slices.Contains(s.Filter.ChildCliqueNames, name) {
			return true
		}
	}
	return false
}
