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

package pods

import (
	"testing"

	v1 "k8s.io/api/core/v1"
)

func TestAtLeastCountInPhases(t *testing.T) {
	pods := podListWithPhases(7, 5, 2)

	tests := []struct {
		name         string
		minimumCount int
		phases       []v1.PodPhase
		want         bool
	}{
		{name: "matches exact count", minimumCount: 7, phases: []v1.PodPhase{v1.PodRunning}, want: true},
		{name: "matches lower count", minimumCount: 6, phases: []v1.PodPhase{v1.PodRunning}, want: true},
		{name: "combines supplied phases", minimumCount: 9, phases: []v1.PodPhase{v1.PodRunning, v1.PodSucceeded}, want: true},
		{name: "rejects insufficient matching pods", minimumCount: 10, phases: []v1.PodPhase{v1.PodRunning, v1.PodSucceeded}, want: false},
		{name: "does not count excluded phases", minimumCount: 6, phases: []v1.PodPhase{v1.PodPending}, want: false},
		{name: "does not double count duplicate phases", minimumCount: 8, phases: []v1.PodPhase{v1.PodRunning, v1.PodRunning}, want: false},
		{name: "zero minimum matches", minimumCount: 0, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AtLeastCountInPhases(tt.minimumCount, tt.phases...)(pods); got != tt.want {
				t.Fatalf("AtLeastCountInPhases(%d, %v) = %t, want %t", tt.minimumCount, tt.phases, got, tt.want)
			}
		})
	}
}

func podListWithPhases(running, pending, succeeded int) *v1.PodList {
	pods := &v1.PodList{Items: make([]v1.Pod, 0, running+pending+succeeded)}
	for range running {
		pods.Items = append(pods.Items, v1.Pod{Status: v1.PodStatus{Phase: v1.PodRunning}})
	}
	for range pending {
		pods.Items = append(pods.Items, v1.Pod{Status: v1.PodStatus{Phase: v1.PodPending}})
	}
	for range succeeded {
		pods.Items = append(pods.Items, v1.Pod{Status: v1.PodStatus{Phase: v1.PodSucceeded}})
	}
	return pods
}
