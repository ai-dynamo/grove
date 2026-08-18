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

// MapContainsAll reports whether target contains every key-value pair from expected.
// Target-only entries are ignored. A non-nil expected map requires a non-nil target.
func MapContainsAll[K comparable, V comparable](target, expected map[K]V) bool {
	if expected != nil && target == nil {
		return false
	}
	for key, expectedValue := range expected {
		if targetValue, exists := target[key]; !exists || targetValue != expectedValue {
			return false
		}
	}
	return true
}
