// /*
// Copyright 2024 The Grove Authors.
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

import "strings"

// IsEmptyStringType returns true if value (which is a string or has an underline type string) is empty or contains only whitespace characters.
func IsEmptyStringType[T ~string](val T) bool {
	return len(strings.TrimSpace(string(val))) == 0
}

// OnlyOneIsNil returns true if only one of the Objects is nil else it will return false.
func OnlyOneIsNil[T any](objA, objB *T) bool {
	return (objA == nil) != (objB == nil)
}

// OnlyOneIsEmpty returns true if only one of the Objects is empty else it will return false.
func OnlyOneIsEmpty[T any](objA, objB []T) bool {
	return (len(objA) == 0) != (len(objB) == 0)
}
