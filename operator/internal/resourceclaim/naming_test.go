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

package resourceclaim

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAllReplicasRCName(t *testing.T) {
	tests := []struct {
		name      string
		ownerName string
		rctName   string
		want      string
	}{
		{
			name:      "pcs owner",
			ownerName: "my-pcs",
			rctName:   "gpu-mps",
			want:      "my-pcs-all-gpu-mps",
		},
		{
			name:      "pcsg owner",
			ownerName: "my-pcs-0-sga",
			rctName:   "shared-mem",
			want:      "my-pcs-0-sga-all-shared-mem",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, AllReplicasRCName(tt.ownerName, tt.rctName))
		})
	}
}

func TestPerReplicaRCName(t *testing.T) {
	tests := []struct {
		name         string
		ownerName    string
		replicaIndex int
		rctName      string
		want         string
	}{
		{
			name:         "replica 0",
			ownerName:    "my-pcs",
			replicaIndex: 0,
			rctName:      "gpu-mps",
			want:         "my-pcs-0-gpu-mps",
		},
		{
			name:         "replica 5",
			ownerName:    "my-pcs",
			replicaIndex: 5,
			rctName:      "gpu-mps",
			want:         "my-pcs-5-gpu-mps",
		},
		{
			name:         "pcsg owner replica 2",
			ownerName:    "my-pcs-0-sga",
			replicaIndex: 2,
			rctName:      "shared-mem",
			want:         "my-pcs-0-sga-2-shared-mem",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, PerReplicaRCName(tt.ownerName, tt.replicaIndex, tt.rctName))
		})
	}
}
