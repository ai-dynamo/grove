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

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewSet(t *testing.T) {
	t.Run("empty input → empty set", func(t *testing.T) {
		got := NewSet[string](nil)
		assert.Equal(t, Set[string]{}, got)
	})
	t.Run("populated input", func(t *testing.T) {
		got := NewSet([]string{"a", "b", "c"})
		assert.True(t, got.Has("a"))
		assert.True(t, got.Has("b"))
		assert.True(t, got.Has("c"))
		assert.False(t, got.Has("d"))
		assert.Len(t, got, 3)
	})
	t.Run("duplicates collapse to a single entry", func(t *testing.T) {
		got := NewSet([]string{"a", "a", "b"})
		assert.Len(t, got, 2)
		assert.True(t, got.Has("a"))
		assert.True(t, got.Has("b"))
	})
}

func TestNewSetBy(t *testing.T) {
	type item struct {
		key  string
		junk int
	}
	t.Run("keyFunc applied to each element", func(t *testing.T) {
		got := NewSetBy([]item{{"a", 1}, {"b", 2}}, func(i item) string { return i.key })
		assert.Equal(t, Set[string]{"a": {}, "b": {}}, got)
	})
	t.Run("duplicate keys collapse", func(t *testing.T) {
		got := NewSetBy([]item{{"a", 1}, {"a", 2}}, func(i item) string { return i.key })
		assert.Len(t, got, 1)
		assert.True(t, got.Has("a"))
	})
}

func TestSetHas(t *testing.T) {
	s := Set[string]{"present": {}}
	assert.True(t, s.Has("present"))
	assert.False(t, s.Has("absent"))
	t.Run("zero-value type", func(t *testing.T) {
		// Zero value of int is 0; verify Has works with the zero value as a real entry.
		zeroSet := Set[int]{0: {}}
		assert.True(t, zeroSet.Has(0))
		assert.False(t, zeroSet.Has(1))
	})
}

func TestMapBy(t *testing.T) {
	type item struct {
		name  string
		value int
	}
	t.Run("empty input → empty map", func(t *testing.T) {
		got := MapBy[item, string, int](nil, func(i item) (string, int) { return i.name, i.value })
		assert.Equal(t, map[string]int{}, got)
	})
	t.Run("populated input", func(t *testing.T) {
		got := MapBy([]item{{"a", 1}, {"b", 2}}, func(i item) (string, int) { return i.name, i.value })
		assert.Equal(t, map[string]int{"a": 1, "b": 2}, got)
	})
	t.Run("duplicate keys: last write wins", func(t *testing.T) {
		// Documented semantic — callers should pre-dedup if they need stricter behavior.
		got := MapBy([]item{{"a", 1}, {"a", 99}}, func(i item) (string, int) { return i.name, i.value })
		assert.Equal(t, map[string]int{"a": 99}, got)
	})
}
