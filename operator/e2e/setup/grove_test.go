//go:build e2e

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

package setup

import (
	"testing"

	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestGroveConfigToHelmValuesEnablesHealthProbes(t *testing.T) {
	values, err := (&GroveConfig{}).toHelmValues(nil)
	require.NoError(t, err)

	enabled, found, err := unstructured.NestedBool(values, "config", "server", "healthProbes", "enable")
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, enabled)

	port, found, err := unstructured.NestedFieldNoCopy(values, "config", "server", "healthProbes", "port")
	require.NoError(t, err)
	require.True(t, found)
	assert.EqualValues(t, DefaultHealthProbePort, port)
}

func TestGroveConfigToHelmValuesCarriesScaleInCostSource(t *testing.T) {
	values, err := (&GroveConfig{ScaleIn: configv1beta1.ScaleInConfiguration{CostSource: configv1beta1.ScaleInCostSourcePodDeletionCost}}).toHelmValues(nil)
	require.NoError(t, err)

	source, found, err := unstructured.NestedString(values, "config", "scaleIn", "costSource")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, string(configv1beta1.ScaleInCostSourcePodDeletionCost), source)
}

func TestGroveConfigToHelmValuesCarriesPodAnnotations(t *testing.T) {
	values, err := (&GroveConfig{}).toHelmValues(map[string]string{OperatorConfiguredAtAnnotation: "2026-11-05T00:00:00Z"})
	require.NoError(t, err)

	stamp, found, err := unstructured.NestedString(values, "podAnnotations", OperatorConfiguredAtAnnotation)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "2026-11-05T00:00:00Z", stamp)
}
