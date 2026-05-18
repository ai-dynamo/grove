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

package kubernetes

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestValidateAnnotationsImmutability(t *testing.T) {
	basePath := field.NewPath("metadata", "annotations")
	keys := []string{"example.io/key-a", "example.io/key-b"}

	tests := []struct {
		description    string
		oldAnnotations map[string]string
		newAnnotations map[string]string
		keys           []string
		expectErrors   int
		errorContains  []string
	}{
		{
			description:    "no annotations on either side -> no errors",
			oldAnnotations: nil,
			newAnnotations: nil,
			keys:           keys,
			expectErrors:   0,
		},
		{
			description:    "both sides have same values -> no errors",
			oldAnnotations: map[string]string{"example.io/key-a": "val1", "example.io/key-b": "val2"},
			newAnnotations: map[string]string{"example.io/key-a": "val1", "example.io/key-b": "val2"},
			keys:           keys,
			expectErrors:   0,
		},
		{
			description:    "annotation added -> error",
			oldAnnotations: map[string]string{},
			newAnnotations: map[string]string{"example.io/key-a": "val1"},
			keys:           keys,
			expectErrors:   1,
			errorContains:  []string{"cannot be added"},
		},
		{
			description:    "annotation removed -> error",
			oldAnnotations: map[string]string{"example.io/key-a": "val1"},
			newAnnotations: map[string]string{},
			keys:           keys,
			expectErrors:   1,
			errorContains:  []string{"cannot be removed"},
		},
		{
			description:    "annotation value changed -> error",
			oldAnnotations: map[string]string{"example.io/key-a": "old"},
			newAnnotations: map[string]string{"example.io/key-a": "new"},
			keys:           keys,
			expectErrors:   1,
			errorContains:  []string{"immutable"},
		},
		{
			description:    "multiple keys violated -> multiple errors",
			oldAnnotations: map[string]string{"example.io/key-a": "v1", "example.io/key-b": "v2"},
			newAnnotations: map[string]string{"example.io/key-a": "changed", "example.io/key-b": "changed"},
			keys:           keys,
			expectErrors:   2,
			errorContains:  []string{"immutable", "immutable"},
		},
		{
			description:    "one key added and one removed -> two errors",
			oldAnnotations: map[string]string{"example.io/key-a": "val"},
			newAnnotations: map[string]string{"example.io/key-b": "val"},
			keys:           keys,
			expectErrors:   2,
			errorContains:  []string{"cannot be removed", "cannot be added"},
		},
		{
			description:    "untracked annotation changes are ignored",
			oldAnnotations: map[string]string{"other/key": "old"},
			newAnnotations: map[string]string{"other/key": "new"},
			keys:           keys,
			expectErrors:   0,
		},
		{
			description:    "nil old, nil new -> no errors",
			oldAnnotations: nil,
			newAnnotations: nil,
			keys:           keys,
			expectErrors:   0,
		},
		{
			description:    "nil old, annotation present in new -> error",
			oldAnnotations: nil,
			newAnnotations: map[string]string{"example.io/key-a": "val"},
			keys:           keys,
			expectErrors:   1,
			errorContains:  []string{"cannot be added"},
		},
		{
			description:    "annotation present in old, nil new -> error",
			oldAnnotations: map[string]string{"example.io/key-a": "val"},
			newAnnotations: nil,
			keys:           keys,
			expectErrors:   1,
			errorContains:  []string{"cannot be removed"},
		},
		{
			description:    "empty keys slice -> no errors regardless of annotations",
			oldAnnotations: map[string]string{"example.io/key-a": "old"},
			newAnnotations: map[string]string{"example.io/key-a": "new"},
			keys:           []string{},
			expectErrors:   0,
		},
		{
			description:    "same value empty string on both sides -> no errors",
			oldAnnotations: map[string]string{"example.io/key-a": ""},
			newAnnotations: map[string]string{"example.io/key-a": ""},
			keys:           keys,
			expectErrors:   0,
		},
		{
			description:    "keys checked in provided order: first unchanged, second changed -> one error on second key",
			oldAnnotations: map[string]string{"example.io/key-a": "same", "example.io/key-b": "old"},
			newAnnotations: map[string]string{"example.io/key-a": "same", "example.io/key-b": "new"},
			keys:           []string{"example.io/key-a", "example.io/key-b"},
			expectErrors:   1,
			errorContains:  []string{"key-b"},
		},
		{
			description:    "reversed key order: first changed, second unchanged -> one error on first key",
			oldAnnotations: map[string]string{"example.io/key-a": "same", "example.io/key-b": "old"},
			newAnnotations: map[string]string{"example.io/key-a": "same", "example.io/key-b": "new"},
			keys:           []string{"example.io/key-b", "example.io/key-a"},
			expectErrors:   1,
			errorContains:  []string{"key-b"},
		},
		{
			description:    "mixed violations in key order: added then changed -> errors in key order",
			oldAnnotations: map[string]string{"example.io/key-b": "old"},
			newAnnotations: map[string]string{"example.io/key-a": "new", "example.io/key-b": "changed"},
			keys:           []string{"example.io/key-a", "example.io/key-b"},
			expectErrors:   2,
			errorContains:  []string{"key-a", "key-b"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			errs := ValidateAnnotationsImmutability(tc.oldAnnotations, tc.newAnnotations, tc.keys, basePath)
			assert.Len(t, errs, tc.expectErrors)
			for i, substr := range tc.errorContains {
				if i < len(errs) {
					assert.Contains(t, errs[i].Error(), substr)
				}
			}
		})
	}
}
