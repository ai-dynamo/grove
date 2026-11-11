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

package controllerrevision

import (
	"encoding/json"
	"testing"

	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/uuid"
)

func TestEncodeDecodeTemplateRoundTrips(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test", "default", uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()

	data, err := EncodeTemplate(&pcs.Spec.Template)
	require.NoError(t, err)

	decoded, err := DecodeTemplate(data)
	require.NoError(t, err)
	assert.Equal(t, *NormalizeTemplate(&pcs.Spec.Template), *decoded)
}

func TestDecodeTemplateRejectsUnknownAPIVersion(t *testing.T) {
	_, err := DecodeTemplate([]byte(`{"apiVersion":"other/v1","podCliqueSetTemplateSpec":{}}`))
	require.ErrorContains(t, err, "unsupported PodCliqueSet revision API version")
}

func TestDecodeTemplateRejectsTrailingData(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test", "default", uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	data, err := EncodeTemplate(&pcs.Spec.Template)
	require.NoError(t, err)

	_, err = DecodeTemplate(append(data, []byte(`{}`)...))
	require.Error(t, err)
}

// TestDecodeTemplateToleratesUnknownFields verifies that a revision written by a future operator,
// carrying payload fields this binary does not know about, is still decoded: the extra keys are
// ignored and the template is recovered. This is the forward-compatibility guarantee relied on
// during operator version skew.
func TestDecodeTemplateToleratesUnknownFields(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test", "default", uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	data, err := EncodeTemplate(&pcs.Spec.Template)
	require.NoError(t, err)

	// Inject an unknown top-level field, mimicking a newer payload shape.
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	payload["futureField"] = "value"
	withUnknown, err := json.Marshal(payload)
	require.NoError(t, err)

	decoded, err := DecodeTemplate(withUnknown)
	require.NoError(t, err)
	assert.Equal(t, *NormalizeTemplate(&pcs.Spec.Template), *decoded)
}

func TestDataMatchesTemplate(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test", "default", uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	data, err := EncodeTemplate(&pcs.Spec.Template)
	require.NoError(t, err)

	match, err := DataMatchesTemplate(data, NormalizeTemplate(&pcs.Spec.Template))
	require.NoError(t, err)
	assert.True(t, match)

	changed := pcs.Spec.Template.DeepCopy()
	changed.Cliques[0].Spec.Replicas = pcs.Spec.Template.Cliques[0].Spec.Replicas + 1
	match, err = DataMatchesTemplate(data, NormalizeTemplate(changed))
	require.NoError(t, err)
	assert.False(t, match)
}
