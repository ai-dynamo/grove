// /*
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
// */

package utils

// Set represents unique values with O(1) membership checks. Use it where callers only need
// to test whether an element is present; use a plain map[K]V (built via MapBy) where callers
// also need to retrieve the value associated with the key.
type Set[T comparable] map[T]struct{}

// NewSet converts a slice into a set for O(1) membership checks.
func NewSet[T comparable](values []T) Set[T] {
	return NewSetBy(values, func(value T) T {
		return value
	})
}

// NewSetBy converts a slice into a set using keyFunc to derive the key.
func NewSetBy[T any, K comparable](values []T, keyFunc func(T) K) Set[K] {
	set := make(Set[K], len(values))
	for _, value := range values {
		set[keyFunc(value)] = struct{}{}
	}
	return set
}

// Has returns true if the set contains value. The Set type uses a sentinel struct{} value, so
// callers cannot use the comma-ok idiom directly; this method gives them an equivalent.
func (s Set[T]) Has(value T) bool {
	_, exists := s[value]
	return exists
}

// MapBy converts a slice into a name-keyed map using mapFunc to derive each key and value.
// Last-write-wins on duplicate keys: callers should ensure inputs are unique by key, otherwise
// only the last entry per key survives.
func MapBy[T any, K comparable, V any](values []T, mapFunc func(T) (K, V)) map[K]V {
	byKey := make(map[K]V, len(values))
	for _, value := range values {
		key, mappedValue := mapFunc(value)
		byKey[key] = mappedValue
	}
	return byKey
}
