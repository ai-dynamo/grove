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
	"encoding/json"
)

// ConvertUnstructuredToTyped converts an unstructured map to a typed object
func ConvertUnstructuredToTyped(u map[string]interface{}, typed interface{}) error {
	data, err := json.Marshal(u)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, typed)
}

// ConvertTypedToUnstructured converts a typed object to an unstructured map.
// Useful for converting typed structs to Helm values maps.
func ConvertTypedToUnstructured(typed interface{}) (map[string]interface{}, error) {
	data, err := json.Marshal(typed)
	if err != nil {
		return nil, err
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}
