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

package validation

import (
	"strings"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfiguredReplicas(t *testing.T) {
	for _, tt := range []struct {
		name    string
		initial int32
		config  *grovecorev1alpha1.AutoScalingConfig
		want    int32
	}{
		{name: "initial replicas without HPA", initial: 3, want: 3},
		{name: "HPA maximum is larger", initial: 3, config: &grovecorev1alpha1.AutoScalingConfig{MaxReplicas: 7}, want: 7},
		{name: "initial replicas are larger", initial: 7, config: &grovecorev1alpha1.AutoScalingConfig{MaxReplicas: 3}, want: 7},
	} {
		t.Run(tt.name, func(t *testing.T) {
			replicas := configuredReplicas(tt.initial, tt.config)
			assert.Equal(t, tt.want, replicas)
		})
	}
}

func TestValidateGeneratedPodNames(t *testing.T) {
	t.Run("hostname at DNS label limit", func(t *testing.T) {
		require.NoError(t, validateGeneratedPodNames(strings.Repeat("a", 59), 1000))
	})

	t.Run("hostname over DNS label limit", func(t *testing.T) {
		require.ErrorContains(t, validateGeneratedPodNames(strings.Repeat("a", 59), 1001), "generated pod hostname")
	})

	t.Run("dot is valid in metadata name but not hostname", func(t *testing.T) {
		require.ErrorContains(t, validateGeneratedPodNames("workload.worker", 1), "generated pod hostname")
	})
}

func TestValidateResourceClaimReferenceName(t *testing.T) {
	allReplicas := grovecorev1alpha1.ResourceSharingSpec{
		Name:  "gpu",
		Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas,
	}
	perReplica := grovecorev1alpha1.ResourceSharingSpec{
		Name:  "gpu",
		Scope: grovecorev1alpha1.ResourceSharingScopePerReplica,
	}

	t.Run("allows names at DNS label limit", func(t *testing.T) {
		require.NoError(t, validateResourceClaimReferenceName(strings.Repeat("a", 55), 1000, &allReplicas))
		require.NoError(t, validateResourceClaimReferenceName(strings.Repeat("a", 55), 1000, &perReplica))
	})

	t.Run("reports generated name over DNS label limit", func(t *testing.T) {
		require.ErrorContains(t,
			validateResourceClaimReferenceName(strings.Repeat("a", 55), 1001, &perReplica),
			"generated pod resource claim reference name",
		)
	})

	t.Run("zero replicas validates owner claim and skips per-replica claim", func(t *testing.T) {
		require.Error(t, validateResourceClaimReferenceName(strings.Repeat("a", 63), 0, &allReplicas))
		require.NoError(t, validateResourceClaimReferenceName(strings.Repeat("a", 63), 0, &perReplica))
	})
}
