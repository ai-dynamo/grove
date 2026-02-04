// /*
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
// */

package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertTypedToUnstructured(t *testing.T) {
	type nested struct {
		Value string `json:"value"`
	}
	type testStruct struct {
		Name   string `json:"name"`
		Count  int    `json:"count"`
		Nested nested `json:"nested"`
	}

	input := testStruct{
		Name:   "test",
		Count:  42,
		Nested: nested{Value: "inner"},
	}

	result, err := ConvertTypedToUnstructured(input)
	require.NoError(t, err)

	assert.Equal(t, "test", result["name"])
	assert.Equal(t, float64(42), result["count"]) // JSON numbers become float64
	assert.Equal(t, "inner", result["nested"].(map[string]interface{})["value"])
}

func TestConvertUnstructuredToTyped(t *testing.T) {
	type testStruct struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	input := map[string]interface{}{
		"name":  "test",
		"count": float64(42),
	}

	var result testStruct
	err := ConvertUnstructuredToTyped(input, &result)
	require.NoError(t, err)

	assert.Equal(t, "test", result.Name)
	assert.Equal(t, 42, result.Count)
}
