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

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"
)

func TestEffectiveReplicas(t *testing.T) {
	tests := []struct {
		name         string
		desired      int32
		minAvailable *int32
		want         int32
	}{
		{name: "zero", desired: 0, minAvailable: ptr.To(int32(2)), want: 0},
		{name: "below", desired: 1, minAvailable: ptr.To(int32(2)), want: 2},
		{name: "equal", desired: 2, minAvailable: ptr.To(int32(2)), want: 2},
		{name: "above", desired: 3, minAvailable: ptr.To(int32(2)), want: 3},
		{name: "nil", desired: 1, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, EffectiveReplicas(tt.desired, tt.minAvailable))
		})
	}
}
