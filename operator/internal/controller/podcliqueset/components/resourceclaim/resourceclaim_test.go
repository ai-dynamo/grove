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

func TestIsStaleReplicaRC_PCSAllReplicas(t *testing.T) {
	assert.False(t, isStaleReplicaRC("my-pcs", 2, "my-pcs-all-gpu"))
}

func TestIsStaleReplicaRC_CurrentReplica(t *testing.T) {
	assert.False(t, isStaleReplicaRC("my-pcs", 2, "my-pcs-0-ext-tpl"))
	assert.False(t, isStaleReplicaRC("my-pcs", 2, "my-pcs-1-ext-tpl"))
}

func TestIsStaleReplicaRC_StaleReplica(t *testing.T) {
	assert.True(t, isStaleReplicaRC("my-pcs", 1, "my-pcs-1-ext-tpl"))
	assert.True(t, isStaleReplicaRC("my-pcs", 1, "my-pcs-2-ext-tpl"))
}

func TestIsStaleReplicaRC_LowerLevelCurrentReplica(t *testing.T) {
	assert.False(t, isStaleReplicaRC("pcs", 2, "pcs-0-worker-a-all-int-tpl"))
	assert.False(t, isStaleReplicaRC("pcs", 2, "pcs-1-sga-all-int-tpl2"))
	assert.False(t, isStaleReplicaRC("pcs", 2, "pcs-0-sga-0-int-tpl3"))
	assert.False(t, isStaleReplicaRC("pcs", 2, "pcs-1-sga-1-int-tpl3"))
}

func TestIsStaleReplicaRC_LowerLevelStaleReplica(t *testing.T) {
	assert.True(t, isStaleReplicaRC("pcs", 1, "pcs-1-worker-a-all-int-tpl"))
	assert.True(t, isStaleReplicaRC("pcs", 1, "pcs-1-sga-all-int-tpl2"))
	assert.True(t, isStaleReplicaRC("pcs", 1, "pcs-1-sga-0-int-tpl3"))
	assert.True(t, isStaleReplicaRC("pcs", 1, "pcs-1-sga-1-int-tpl3"))
}

func TestIsStaleReplicaRC_UnrelatedName(t *testing.T) {
	assert.False(t, isStaleReplicaRC("pcs", 1, "other-resource-claim"))
	assert.False(t, isStaleReplicaRC("pcs", 1, "pcs-something"))
}

func TestIsStaleReplicaRC_ScaleToZero(t *testing.T) {
	assert.True(t, isStaleReplicaRC("pcs", 0, "pcs-0-ext-tpl"))
	assert.False(t, isStaleReplicaRC("pcs", 0, "pcs-all-gpu"))
}

func TestIsStaleReplicaRC_MultiLevel(t *testing.T) {
	// Scale from 2 to 1: all rep-1 RCs across all levels should be stale
	staleNames := []string{
		"rs-test-1-ext-tpl",
		"rs-test-1-worker-a-all-int-tpl",
		"rs-test-1-sga-all-int-tpl2",
		"rs-test-1-sga-0-int-tpl3",
		"rs-test-1-sga-1-int-tpl3",
	}
	for _, name := range staleNames {
		assert.True(t, isStaleReplicaRC("rs-test", 1, name), "should be stale: %s", name)
	}

	// All rep-0 RCs and PCS AllReplicas should NOT be stale
	currentNames := []string{
		"rs-test-all-int-tpl",
		"rs-test-0-ext-tpl",
		"rs-test-0-worker-a-all-int-tpl",
		"rs-test-0-sga-all-int-tpl2",
		"rs-test-0-sga-0-int-tpl3",
		"rs-test-0-sga-1-int-tpl3",
	}
	for _, name := range currentNames {
		assert.False(t, isStaleReplicaRC("rs-test", 1, name), "should NOT be stale: %s", name)
	}
}
