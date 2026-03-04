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

package utils

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScaleTestResult_WriteSummary(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	result := &ScaleTestResult{
		TestName:            "ScaleTest_1000",
		RunID:               "scale-1",
		Namespace:           "default",
		TotalExpectedPods:   1000,
		PCSCount:            50,
		NodeCount:           10,
		TestDurationSeconds: 72.5,
		Phases: []Phase{
			{
				Name:                  "deploy",
				StartTime:             start,
				DurationFromTestStart: 0.0,
				Milestones: []Milestone{
					{Name: "all-pods-created", Timestamp: start.Add(10 * time.Second), DurationFromPhaseStart: 10.0},
					{Name: "all-pods-ready", Timestamp: start.Add(15 * time.Second), DurationFromPhaseStart: 15.0},
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := result.WriteSummary(&buf); err != nil {
		t.Fatalf("WriteSummary() error = %v", err)
	}

	out := buf.String()
	assertContains(t, out, "ScaleTest_1000")
	assertContains(t, out, "run: scale-1")
	assertContains(t, out, "Expected pods:    1000")
	assertContains(t, out, "Phase: deploy")
	assertContains(t, out, "all-pods-ready")
}

func TestScaleTestResult_WriteJSON(t *testing.T) {
	t.Parallel()

	result := &ScaleTestResult{
		TestName:            "ScaleTest_100",
		RunID:               "run-123",
		Namespace:           "default",
		TotalExpectedPods:   100,
		PCSCount:            5,
		NodeCount:           3,
		ClusterProfile:      "dev",
		TestDurationSeconds: 12.25,
	}

	var buf bytes.Buffer
	if err := result.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON() error = %v", err)
	}

	var got ScaleTestResult
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if got.RunID != result.RunID {
		t.Fatalf("got RunID %q, want %q", got.RunID, result.RunID)
	}
	if got.TotalExpectedPods != result.TotalExpectedPods {
		t.Fatalf("got TotalExpectedPods %d, want %d", got.TotalExpectedPods, result.TotalExpectedPods)
	}
}

func TestScaleTestResult_SaveToDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	result := &ScaleTestResult{
		TestName:          "ScaleTest_100",
		RunID:             "save-test",
		Namespace:         "default",
		TotalExpectedPods: 100,
		PCSCount:          10,
		NodeCount:         2,
	}

	if err := result.SaveToDir(dir); err != nil {
		t.Fatalf("SaveToDir() error = %v", err)
	}

	summaryPath := filepath.Join(dir, "summary-save-test.txt")
	jsonPath := filepath.Join(dir, "results-save-test.json")

	if _, err := os.Stat(summaryPath); err != nil {
		t.Fatalf("summary file missing: %v", err)
	}
	if _, err := os.Stat(jsonPath); err != nil {
		t.Fatalf("json file missing: %v", err)
	}
}

func assertContains(t *testing.T, s string, want string) {
	t.Helper()
	if !strings.Contains(s, want) {
		t.Fatalf("expected output to contain %q\noutput:\n%s", want, s)
	}
}
