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
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsUnschedulableEvent(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		reason    string
		component string
		reporter  string
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
			name:      "default scheduler reporting controller",
			eventType: v1.EventTypeWarning,
			reason:    failedSchedulingReason,
			reporter:  defaultSchedulerComponent,
			want:      true,
		},
		{
			name:      "reporting controller takes precedence over legacy source",
			eventType: v1.EventTypeWarning,
			reason:    failedSchedulingReason,
			component: defaultSchedulerComponent,
			reporter:  "custom-scheduler",
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
				Type:                tt.eventType,
				Reason:              tt.reason,
				Source:              v1.EventSource{Component: tt.component},
				ReportingController: tt.reporter,
			}
			if got := isUnschedulableEvent(event); got != tt.want {
				t.Fatalf("isUnschedulableEvent() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestEventLastObservedTime(t *testing.T) {
	first := time.Unix(1, 0)
	last := time.Unix(2, 0)
	tests := []struct {
		name  string
		event v1.Event
		want  time.Time
	}{
		{
			name:  "legacy event",
			event: v1.Event{FirstTimestamp: metav1.NewTime(first), LastTimestamp: metav1.NewTime(last)},
			want:  last,
		},
		{
			name:  "singleton event",
			event: v1.Event{EventTime: metav1.NewMicroTime(first)},
			want:  first,
		},
		{
			name: "event series",
			event: v1.Event{
				EventTime: metav1.NewMicroTime(first),
				Series:    &v1.EventSeries{Count: 2, LastObservedTime: metav1.NewMicroTime(last)},
			},
			want: last,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := eventLastObservedTime(&tt.event); !got.Equal(tt.want) {
				t.Fatalf("eventLastObservedTime() = %v, want %v", got, tt.want)
			}
		})
	}
}
