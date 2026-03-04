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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ScaleTestResult accumulates all timeline/raw measurement data for a single run.
// It is intended for sequential writes in tests and is not thread-safe.
type ScaleTestResult struct {
	// Test identity.
	TestName  string `json:"testName"`
	RunID     string `json:"runID"`
	Namespace string `json:"namespace"`

	// Scale metadata.
	TotalExpectedPods int    `json:"totalExpectedPods"`
	PCSCount          int    `json:"pcsCount"`
	NodeCount         int    `json:"nodeCount"`
	ClusterProfile    string `json:"clusterProfile,omitempty"`

	// Timeline data from TimelineTracker.
	Phases              []Phase `json:"phases"`
	TestDurationSeconds float64 `json:"testDurationSeconds"`
}

// WriteSummary writes a human-readable summary of the run.
func (r *ScaleTestResult) WriteSummary(w io.Writer) error {
	if w == nil {
		return fmt.Errorf("summary writer cannot be nil")
	}

	if _, err := fmt.Fprintf(w, "=== Scale Test: %s (run: %s) ===\n", r.TestName, r.RunID); err != nil {
		return fmt.Errorf("write summary header: %w", err)
	}
	if _, err := fmt.Fprintf(w, "Namespace:        %s\n", r.Namespace); err != nil {
		return fmt.Errorf("write summary namespace: %w", err)
	}
	if _, err := fmt.Fprintf(w, "Node count:       %d\n", r.NodeCount); err != nil {
		return fmt.Errorf("write summary node count: %w", err)
	}
	if _, err := fmt.Fprintf(w, "PCS count:        %d\n", r.PCSCount); err != nil {
		return fmt.Errorf("write summary pcs count: %w", err)
	}
	if _, err := fmt.Fprintf(w, "Expected pods:    %d\n", r.TotalExpectedPods); err != nil {
		return fmt.Errorf("write summary expected pods: %w", err)
	}
	if _, err := fmt.Fprintf(w, "Total test time:  %.3fs\n", r.TestDurationSeconds); err != nil {
		return fmt.Errorf("write summary total duration: %w", err)
	}

	if _, err := io.WriteString(w, "Timeline:\n"); err != nil {
		return fmt.Errorf("write summary timeline header: %w", err)
	}

	for _, phase := range r.Phases {
		if _, err := fmt.Fprintf(
			w,
			"  Phase: %s (started +%.3fs)\n",
			phase.Name,
			phase.DurationFromTestStart,
		); err != nil {
			return fmt.Errorf("write summary phase %q: %w", phase.Name, err)
		}

		for _, milestone := range phase.Milestones {
			if _, err := fmt.Fprintf(
				w,
				"    %s  +%.3fs\n",
				milestone.Name,
				milestone.DurationFromPhaseStart,
			); err != nil {
				return fmt.Errorf("write summary phase %q milestone %q: %w", phase.Name, milestone.Name, err)
			}
		}
	}

	return nil
}

// WriteJSON writes the result as indented JSON.
func (r *ScaleTestResult) WriteJSON(w io.Writer) error {
	if w == nil {
		return fmt.Errorf("json writer cannot be nil")
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return fmt.Errorf("encode scale test result json: %w", err)
	}
	return nil
}

// SaveToDir creates the output directory and writes summary/json files:
//
//	<dir>/summary-<runID>.txt
//	<dir>/results-<runID>.json
func (r *ScaleTestResult) SaveToDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("output directory cannot be empty")
	}
	if r.RunID == "" {
		return fmt.Errorf("run id cannot be empty")
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create output directory %q: %w", dir, err)
	}

	summaryPath := filepath.Join(dir, fmt.Sprintf("summary-%s.txt", r.RunID))
	summaryFile, err := os.Create(summaryPath)
	if err != nil {
		return fmt.Errorf("create summary file %q: %w", summaryPath, err)
	}
	if err := r.WriteSummary(summaryFile); err != nil {
		_ = summaryFile.Close()
		return fmt.Errorf("write summary file %q: %w", summaryPath, err)
	}
	if err := summaryFile.Close(); err != nil {
		return fmt.Errorf("close summary file %q: %w", summaryPath, err)
	}

	jsonPath := filepath.Join(dir, fmt.Sprintf("results-%s.json", r.RunID))
	jsonFile, err := os.Create(jsonPath)
	if err != nil {
		return fmt.Errorf("create json file %q: %w", jsonPath, err)
	}
	if err := r.WriteJSON(jsonFile); err != nil {
		_ = jsonFile.Close()
		return fmt.Errorf("write json file %q: %w", jsonPath, err)
	}
	if err := jsonFile.Close(); err != nil {
		return fmt.Errorf("close json file %q: %w", jsonPath, err)
	}

	return nil
}
