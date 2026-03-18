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
	"errors"
	"os"
	"path/filepath"
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
		K8sClient: &measurement.K8sClientConfig{
			QPS:   100,
			Burst: 150,
		},
		ControllerMaxReconcile: &measurement.ControllerMaxReconcile{
			PodCliqueSet:          1,
			PodCliqueScalingGroup: 1,
			PodClique:             1,
		},
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
	assertContains(t, out, "K8s client:")
	assertContains(t, out, "QPS=100")
	assertContains(t, out, "Burst=150")
	assertContains(t, out, "Max reconcile:")
	assertContains(t, out, "pcs=1")
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

	jsonPath := filepath.Join(t.TempDir(), "result.json")
	var summaryBuf bytes.Buffer
	multi := NewMultiExporter(
		NewJSONFileExporter(jsonPath),
		NewSummaryExporter(&summaryBuf),
	)

	if err := multi.Export(result); err != nil {
		t.Fatalf("MultiExporter.Export() error = %v", err)
	}

	jsonData, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	if len(jsonData) == 0 {
		t.Fatal("JSONExporter produced no output")
	}
	if summaryBuf.Len() == 0 {
		t.Fatal("SummaryExporter produced no output")
	}

	var got measurement.TrackerResult
	if err := json.Unmarshal(jsonData, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got.RunID != "multi-test" {
		t.Fatalf("got RunID %q, want %q", got.RunID, "multi-test")
	}

	assertContains(t, summaryBuf.String(), "ScaleTest_100")
}

type errExporter struct{ msg string }

func (e *errExporter) Export(_ *measurement.TrackerResult) error { return errors.New(e.msg) }

func TestMultiExporter_ErrorAggregation(t *testing.T) {
	t.Parallel()

	result := &measurement.TrackerResult{RunID: "err-test"}

	tests := []struct {
		name       string
		exporters  []ResultExporter
		wantErr    bool
		wantMsg    string
		wantAllRun bool
	}{
		{
			name: "single failure returns error",
			exporters: []ResultExporter{
				&errExporter{msg: "disk full"},
			},
			wantErr: true,
			wantMsg: "disk full",
		},
		{
			name: "continues after first error",
			exporters: []ResultExporter{
				&errExporter{msg: "first error"},
				&errExporter{msg: "second error"},
			},
			wantErr:    true,
			wantMsg:    "first error",
			wantAllRun: true,
		},
		{
			name: "no errors returns nil",
			exporters: []ResultExporter{
				NewJSONExporter(&bytes.Buffer{}),
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := NewMultiExporter(tc.exporters...).Export(result)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if tc.wantMsg != "" && !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantMsg)
			}
			if tc.wantAllRun {
				for _, exp := range tc.exporters {
					ee := exp.(*errExporter)
					if !strings.Contains(err.Error(), ee.msg) {
						t.Fatalf("error %q missing message from %q", err.Error(), ee.msg)
					}
				}
			}
		})
	}
}

func assertContains(t *testing.T, s string, want string) {
	t.Helper()
	if !strings.Contains(s, want) {
		t.Fatalf("expected output to contain %q\noutput:\n%s", want, s)
	}
}
