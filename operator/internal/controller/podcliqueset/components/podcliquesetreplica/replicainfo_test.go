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

package podcliquesetreplica

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	"github.com/stretchr/testify/assert"
)

func TestHasMaterializedPCSGeneration(t *testing.T) {
	cases := []struct {
		name        string
		pclqMarker  string
		pcsgMarker  string
		includePCLQ bool
		includePCSG bool
		hash        string
		want        bool
	}{
		{name: "both templates applied before either status converges", pclqMarker: "S1", pcsgMarker: "S1", includePCLQ: true, includePCSG: true, hash: "S1", want: true},
		{name: "old standalone spec", pclqMarker: "S0", pcsgMarker: "S1", includePCLQ: true, includePCSG: true, hash: "S1"},
		{name: "old group spec despite current members", pclqMarker: "S1", pcsgMarker: "S0", includePCLQ: true, includePCSG: true, hash: "S1"},
		{name: "unknown standalone marker", pcsgMarker: "S1", includePCLQ: true, includePCSG: true, hash: "S1"},
		{name: "unknown group marker", pclqMarker: "S1", includePCLQ: true, includePCSG: true, hash: "S1"},
		{name: "missing standalone", pcsgMarker: "S1", includePCSG: true, hash: "S1"},
		{name: "missing group", pclqMarker: "S1", includePCLQ: true, hash: "S1"},
		{name: "no resources", hash: "S1"},
		{name: "unknown target cannot match absent markers", includePCLQ: true, includePCSG: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcs := replicaRecreationSelectionTestPCS()
			info := replicaRecreationSelectionInfo(pcs, 0, "recorded-status", tc.includePCLQ, tc.includePCSG)
			for i := range info.standalonePCLQs {
				info.standalonePCLQs[i].Labels[apicommon.LabelPodCliqueSetGenerationHash] = tc.pclqMarker
				info.standalonePCLQs[i].Status = grovecorev1beta1.PodCliqueStatus{}
			}
			for i := range info.pcsgs {
				info.pcsgs[i].Labels[apicommon.LabelPodCliqueSetGenerationHash] = tc.pcsgMarker
				info.pcsgs[i].Status = grovecorev1beta1.PodCliqueScalingGroupStatus{}
			}
			assert.Equal(t, tc.want, info.hasMaterializedPCSGeneration(pcs, tc.hash))
			assert.False(t, info.hasConvergedToPCSGeneration(pcs, tc.hash), "materialization cannot imply completion")
		})
	}
}
