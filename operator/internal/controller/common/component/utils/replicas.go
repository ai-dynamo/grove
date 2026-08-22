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

package utils

// EffectiveReplicas keeps spec.replicas as the autoscaler-owned desired value while
// clamping the controller target to quorum; desired zero remains intentional idle.
// Unlike GREP-0677's wake-up-only clamp, this intentionally
// applies to both scale-out and active scale-in: rejecting below-quorum scale-in
// makes replica writers retry denied writes without reducing capacity.
func EffectiveReplicas(desired int32, minAvailable *int32) int32 {
	if desired == 0 || minAvailable == nil {
		return desired
	}
	return max(desired, *minAvailable)
}
