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

func TestIsUnschedulableEvent(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		reason    string
		component string
		want      bool
	}{
		{
			name:      "default scheduler",
			eventType: v1.EventTypeWarning,
			reason:    failedSchedulingReason,
			component: defaultSchedulerComponent,
			want:      true,
		},
		{
			name:      "KAI scheduler",
			eventType: v1.EventTypeWarning,
			reason:    unschedulableReason,
			component: kaiSchedulerComponent,
			want:      true,
		},
		{
			name:      "pod grouper",
			eventType: v1.EventTypeWarning,
			reason:    podGrouperWarningReason,
			component: podGrouperComponent,
			want:      true,
		},
		{
			name:      "normal event",
			eventType: v1.EventTypeNormal,
			reason:    failedSchedulingReason,
			component: defaultSchedulerComponent,
		},
		{
			name:      "wrong reason",
			eventType: v1.EventTypeWarning,
			reason:    unschedulableReason,
			component: defaultSchedulerComponent,
		},
		{
			name:      "unknown component",
			eventType: v1.EventTypeWarning,
			reason:    failedSchedulingReason,
			component: "custom-scheduler",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := &v1.Event{
				Type:   tt.eventType,
				Reason: tt.reason,
				Source: v1.EventSource{Component: tt.component},
			}
			if got := isUnschedulableEvent(event); got != tt.want {
				t.Fatalf("isUnschedulableEvent() = %t, want %t", got, tt.want)
			}
		})
	}
}
