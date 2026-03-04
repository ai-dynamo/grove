// /*
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
// */

package exporter

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/utils/measurement"
)

func TestSummaryExporter_Export(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	result := &measurement.TrackerResult{
		TestName:            "ScaleTest_1000",
		RunID:               "scale-1",
		Namespace:           "default",
		PCSCount:            50,
		TestDurationSeconds: 72.5,
		Phases: []measurement.Phase{
			{
				Name:                  "deploy",
				StartTime:             start,
				DurationFromTestStart: 0.0,
				Milestones: []measurement.Milestone{
					{Name: "all-pods-created", Timestamp: start.Add(10 * time.Second), DurationFromPhaseStart: 10.0},
					{Name: "all-pods-ready", Timestamp: start.Add(15 * time.Second), DurationFromPhaseStart: 15.0},
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := NewSummaryExporter(&buf).Export(result); err != nil {
		t.Fatalf("SummaryExporter.Export() error = %v", err)
	}

	out := buf.String()
	assertContains(t, out, "ScaleTest_1000")
	assertContains(t, out, "run: scale-1")
	assertContains(t, out, "PCS count:        50")
	assertContains(t, out, "Phase: deploy")
	assertContains(t, out, "all-pods-ready")
}

func TestJSONExporter_Export(t *testing.T) {
	t.Parallel()

	result := &measurement.TrackerResult{
		TestName:            "ScaleTest_100",
		RunID:               "run-123",
		Namespace:           "default",
		PCSCount:            5,
		TestDurationSeconds: 12.25,
	}

	var buf bytes.Buffer
	if err := NewJSONExporter(&buf).Export(result); err != nil {
		t.Fatalf("JSONExporter.Export() error = %v", err)
	}

	var got measurement.TrackerResult
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if got.RunID != result.RunID {
		t.Fatalf("got RunID %q, want %q", got.RunID, result.RunID)
	}
	if got.PCSCount != result.PCSCount {
		t.Fatalf("got PCSCount %d, want %d", got.PCSCount, result.PCSCount)
	}
}

func TestMultiExporter_Export(t *testing.T) {
	t.Parallel()

	result := &measurement.TrackerResult{
		TestName:  "ScaleTest_100",
		RunID:     "multi-test",
		Namespace: "default",
		PCSCount:  10,
	}

	var jsonBuf, summaryBuf bytes.Buffer
	multi := NewMultiExporter(
		NewJSONExporter(&jsonBuf),
		NewSummaryExporter(&summaryBuf),
	)

	if err := multi.Export(result); err != nil {
		t.Fatalf("MultiExporter.Export() error = %v", err)
	}

	if jsonBuf.Len() == 0 {
		t.Fatal("JSONExporter produced no output")
	}
	if summaryBuf.Len() == 0 {
		t.Fatal("SummaryExporter produced no output")
	}

	var got measurement.TrackerResult
	if err := json.Unmarshal(jsonBuf.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got.RunID != "multi-test" {
		t.Fatalf("got RunID %q, want %q", got.RunID, "multi-test")
	}

	assertContains(t, summaryBuf.String(), "ScaleTest_100")
}

func assertContains(t *testing.T, s string, want string) {
	t.Helper()
	if !strings.Contains(s, want) {
		t.Fatalf("expected output to contain %q\noutput:\n%s", want, s)
	}
}
