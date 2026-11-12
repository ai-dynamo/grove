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

package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// livenessRestartDelay is the earliest time after container start at which the kubelet restarts the
// operator when the health endpoint never comes up, with the liveness probe from the Helm chart:
// initialDelaySeconds of 15 plus two more probe periods of 10 seconds to reach the failure threshold
// of 3.
const livenessRestartDelay = 35 * time.Second

// TestTopologySyncBackoffStaysBelowLivenessRestart verifies that the longest total wait of the
// startup topology synchronization retries, with maximum jitter, is shorter than the time after
// which the kubelet restarts the container.
func TestTopologySyncBackoffStaysBelowLivenessRestart(t *testing.T) {
	var total time.Duration
	wait := float64(topologySyncBackoff.Duration)
	for i := 0; i < topologySyncBackoff.Steps-1; i++ {
		total += time.Duration(wait * (1 + topologySyncBackoff.Jitter))
		wait *= topologySyncBackoff.Factor
	}

	assert.Positive(t, topologySyncBackoff.Steps)
	assert.Less(t, total, livenessRestartDelay)
}
