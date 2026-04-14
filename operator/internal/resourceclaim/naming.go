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

import "fmt"

// RC naming convention:
//
//	AllReplicas: <ownerName>-all-<rctName>
//	PerReplica:  <ownerName>-<replicaIndex>-<rctName>
//
// The "all" keyword serves as a delimiter for AllReplicas-scope names. Since
// Kubernetes names only allow [a-z0-9-], there is no special character available
// as an unambiguous separator. If a PCS name contains "-all-" (e.g. "my-all-pcs"),
// a theoretical collision is possible with another PCS whose name/template
// combination produces the same concatenated string. In practice this requires
// two PCS instances in the same namespace with overlapping owner+template name
// segments, which is extremely unlikely.

// AllReplicasRCName returns the deterministic name for an AllReplicas-scope ResourceClaim.
// Format: <ownerName>-all-<rctName>
func AllReplicasRCName(ownerName, rctName string) string {
	return fmt.Sprintf("%s-all-%s", ownerName, rctName)
}

// PerReplicaRCName returns the deterministic name for a PerReplica-scope ResourceClaim.
// Format: <ownerName>-<replicaIndex>-<rctName>
func PerReplicaRCName(ownerName string, replicaIndex int, rctName string) string {
	return fmt.Sprintf("%s-%d-%s", ownerName, replicaIndex, rctName)
}
