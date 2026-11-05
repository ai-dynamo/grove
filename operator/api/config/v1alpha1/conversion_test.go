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

package v1alpha1

import (
	"testing"

	"github.com/ai-dynamo/grove/operator/api/config/v1beta1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConvertOperatorConfigurationDropsScaleIn pins the one field the versions do not share: a v1beta1
// scale-in configuration has no v1alpha1 counterpart, so it is dropped on the way down and none on the way up.
func TestConvertOperatorConfigurationDropsScaleIn(t *testing.T) {
	in := &v1beta1.OperatorConfiguration{
		LogLevel: v1beta1.DebugLevel,
		ScaleIn:  v1beta1.ScaleInConfiguration{CostSource: v1beta1.ScaleInCostSourcePodDeletionCost},
	}

	down := &OperatorConfiguration{}
	require.NoError(t, Convert_v1beta1_OperatorConfiguration_To_v1alpha1_OperatorConfiguration(in, down, nil))
	assert.Equal(t, LogLevel(v1beta1.DebugLevel), down.LogLevel)

	up := &v1beta1.OperatorConfiguration{}
	require.NoError(t, Convert_v1alpha1_OperatorConfiguration_To_v1beta1_OperatorConfiguration(down, up, nil))
	assert.Equal(t, v1beta1.DebugLevel, up.LogLevel)
	assert.Equal(t, v1beta1.ScaleInCostSourceNone, up.ScaleIn.CostSource)
}
