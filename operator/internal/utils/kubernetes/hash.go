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

package kubernetes

import (
	"fmt"
	"hash/fnv"

	"k8s.io/apimachinery/pkg/util/dump"
	"k8s.io/apimachinery/pkg/util/rand"
)

// ComputeHash computes a deterministic hash from the supplied values.
func ComputeHash[T any](values ...T) string {
	hasher := fnv.New64a()
	hasher.Reset()
	for _, value := range values {
		_, _ = fmt.Fprintf(hasher, "%v", dump.ForHash(value))
	}
	return rand.SafeEncodeString(fmt.Sprint(hasher.Sum64()))
}
