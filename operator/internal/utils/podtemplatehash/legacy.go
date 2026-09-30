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

package podtemplatehash

import (
	"fmt"
	"hash/fnv"
	"reflect"

	legacyv1 "github.com/ai-dynamo/grove/operator/internal/utils/podtemplatehash/legacy/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/dump"
	"k8s.io/apimachinery/pkg/util/rand"
)

// LegacyHashes returns the identities pre-revision operators could have assigned
// to these exact inputs, in Kubernetes 1.35 then 1.34 order. The 1.34 identity is
// omitted if a new field cannot be represented. Only use this during adoption.
func LegacyHashes(templates ...*corev1.PodTemplateSpec) []string {
	previousHasher := fnv.New64a()
	for _, template := range templates {
		if template == nil {
			return nil
		}
		previous := &legacyv1.PodTemplateSpec{}
		if !copyLegacyHashInput(reflect.ValueOf(previous).Elem(), reflect.ValueOf(template).Elem()) {
			return []string{Compute(templates...)}
		}
		_, _ = fmt.Fprint(previousHasher, dump.ForHash(previous))
	}
	return []string{Compute(templates...), rand.SafeEncodeString(fmt.Sprint(previousHasher.Sum64()))}
}

// A JSON round trip would lose nil/empty distinctions and Quantity internals
// included by dump.ForHash. Copy matching types without normalizing their data.
func copyLegacyHashInput(dst, src reflect.Value) bool {
	if dst.Type() == src.Type() {
		dst.Set(src)
		return true
	}
	switch dst.Kind() {
	case reflect.Struct:
		for i := range src.NumField() {
			dstField := dst.FieldByName(src.Type().Field(i).Name)
			if !dstField.IsValid() {
				if !src.Field(i).IsZero() {
					return false
				}
				continue
			}
			if !copyLegacyHashInput(dstField, src.Field(i)) {
				return false
			}
		}
	case reflect.Pointer:
		if !src.IsNil() {
			dst.Set(reflect.New(dst.Type().Elem()))
			return copyLegacyHashInput(dst.Elem(), src.Elem())
		}
	case reflect.Slice:
		if !src.IsNil() {
			dst.Set(reflect.MakeSlice(dst.Type(), src.Len(), src.Len()))
			for i := range src.Len() {
				if !copyLegacyHashInput(dst.Index(i), src.Index(i)) {
					return false
				}
			}
		}
	default:
		return false
	}
	return true
}
