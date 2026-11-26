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

package update

import "testing"

// The Test_KueueRealUpdate* scenarios repeat Test_KueueUpdate* on a real-node cluster (make run-e2e-kueue-real-full
// TEST_PATTERN='^Test_KueueRealUpdate'). KWOK deletes a pod instantly, while here each old pod drains for 10s, so
// Grove must hold every replacement until its predecessor is gone or Kueue finishes the Workload as OutOfSync.

// Test_KueueRealUpdate1_RollingRecreateKeepsWorkload is Test_KueueUpdate1 with pods that drain before they exit.
func Test_KueueRealUpdate1_RollingRecreateKeepsWorkload(t *testing.T) {
	testKueueRollingRecreateKeepsWorkload(t, kueueUpdateFixture{name: "kueue-real-update-rr", yamlPath: "../../yaml/kueue-real-update-rr.yaml", workerNodes: 4})
}

// Test_KueueRealUpdate2_CoherentReplacesWorkload is Test_KueueUpdate2 with pods that drain before they exit.
func Test_KueueRealUpdate2_CoherentReplacesWorkload(t *testing.T) {
	testKueueCoherentReplacesWorkload(t, kueueUpdateFixture{name: "kueue-real-update-coherent", yamlPath: "../../yaml/kueue-real-update-coherent.yaml", workerNodes: 4})
}
