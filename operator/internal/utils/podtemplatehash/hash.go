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

// Package podtemplatehash owns the canonical hashing of Grove pod templates.
package podtemplatehash

import (
	"fmt"
	"hash/fnv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/dump"
	"k8s.io/apimachinery/pkg/util/rand"
)

// Compute returns the canonical hash for one or more PodTemplateSpec.
func Compute(podTemplateSpecs ...*corev1.PodTemplateSpec) string {
	hasher := fnv.New64a()
	for _, podTemplateSpec := range podTemplateSpecs {
		_, _ = fmt.Fprintf(hasher, "%v", dump.ForHash(podTemplateSpec))
	}
	return rand.SafeEncodeString(fmt.Sprint(hasher.Sum64()))
}
