// /*
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
// */

package diagnostics

import (
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CollectEvents dumps Kubernetes events from the last EventLookbackDuration.
func CollectEvents(dc *DiagnosticContext, output DiagnosticOutput) error {
	if dc.Clientset == nil {
		return fmt.Errorf("clientset is nil, cannot list events")
	}

	if err := output.WriteSection(fmt.Sprintf("KUBERNETES EVENTS (last %v)", EventLookbackDuration)); err != nil {
		return err
	}

	_ = output.WriteLinef("[INFO] Listing events in namespace %s...", dc.Namespace)
	events, err := dc.Clientset.CoreV1().Events(dc.Namespace).List(dc.Ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list events: %w", err)
	}

	// Filter to recent events
	cutoff := time.Now().Add(-EventLookbackDuration)
	recentEvents := filterRecentEvents(events.Items, cutoff)

	if len(recentEvents) == 0 {
		_ = output.WriteLinef("[INFO] No events found in namespace %s within last %v", dc.Namespace, EventLookbackDuration)
		return nil
	}

	// Sort by timestamp (oldest first)
	sortEventsByTime(recentEvents)

	// Build table data
	headers := []string{"TIME", "TYPE", "REASON", "OBJECT", "MESSAGE"}
	rows := make([][]string, 0, len(recentEvents))

	for _, event := range recentEvents {
		row := buildEventRow(&event)
		rows = append(rows, row)
	}

	if err := output.WriteTable(headers, rows); err != nil {
		return fmt.Errorf("failed to write events table: %w", err)
	}

	return nil
}

// filterRecentEvents filters events to only those after the cutoff time
func filterRecentEvents(events []corev1.Event, cutoff time.Time) []corev1.Event {
	var recentEvents []corev1.Event
	for _, event := range events {
		eventTime := getEventTime(&event)
		if eventTime.After(cutoff) {
			recentEvents = append(recentEvents, event)
		}
	}
	return recentEvents
}

// sortEventsByTime sorts events by timestamp (oldest first)
func sortEventsByTime(events []corev1.Event) {
	sort.Slice(events, func(i, j int) bool {
		ti := getEventTime(&events[i])
		tj := getEventTime(&events[j])
		return ti.Before(tj)
	})
}

// getEventTime returns the most appropriate timestamp for an event
func getEventTime(event *corev1.Event) time.Time {
	if !event.LastTimestamp.Time.IsZero() {
		return event.LastTimestamp.Time
	}
	return event.EventTime.Time
}

// buildEventRow builds a table row for an event
func buildEventRow(event *corev1.Event) []string {
	eventTime := getEventTime(event)
	timeStr := eventTime.Format(time.RFC3339)
	objectRef := fmt.Sprintf("%s/%s", event.InvolvedObject.Kind, event.InvolvedObject.Name)

	// Truncate message if too long
	message := event.Message
	if len(message) > 80 {
		message = message[:77] + "..."
	}

	return []string{
		timeStr,
		event.Type,
		truncateString(event.Reason, 25),
		truncateString(objectRef, 35),
		message,
	}
}
