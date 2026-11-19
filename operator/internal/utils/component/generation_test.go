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

package component

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSetMaterializedPCSGenerationHash(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		hash   string
	}{
		{name: "nil labels initializes marker", hash: "S1"},
		{name: "existing labels are retained", labels: map[string]string{"other": "value"}, hash: "S1"},
		{name: "new revision replaces old marker", labels: map[string]string{apicommon.LabelPodCliqueSetGenerationHash: "S0"}, hash: "S1"},
		{name: "unknown generation removes marker only", labels: map[string]string{"other": "value", apicommon.LabelPodCliqueSetGenerationHash: "S0"}},
		{name: "unknown generation with nil labels is safe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			meta := metav1.ObjectMeta{Labels: tc.labels}
			SetMaterializedPCSGenerationHash(&meta, tc.hash)
			assert.Equal(t, tc.hash, meta.Labels[apicommon.LabelPodCliqueSetGenerationHash])
			if tc.hash == "" {
				assert.NotContains(t, meta.Labels, apicommon.LabelPodCliqueSetGenerationHash)
			}
			if _, exists := tc.labels["other"]; exists {
				assert.Equal(t, "value", meta.Labels["other"])
			}
		})
	}
}
