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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ai-dynamo/grove/operator/e2e/measurement"
)

// ResultExporter writes a TrackerResult to a destination.
type ResultExporter interface {
	Export(r *measurement.TrackerResult) error
}

// MultiExporter wraps multiple ResultExporters and calls each in order.
// Like io.MultiWriter, it is itself a ResultExporter.
type MultiExporter struct {
	exporters []ResultExporter
}

// NewMultiExporter creates a MultiExporter that delegates to all provided exporters.
func NewMultiExporter(exporters ...ResultExporter) *MultiExporter {
	return &MultiExporter{exporters: exporters}
}

// Export calls Export on each exporter regardless of prior errors (like io.MultiWriter),
// collecting all errors via errors.Join.
func (m *MultiExporter) Export(r *measurement.TrackerResult) error {
	var errs []error
	for _, exp := range m.exporters {
		if err := exp.Export(r); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// JSONExporter writes the result as indented JSON to the configured writer.
type JSONExporter struct {
	w io.Writer
}

// NewJSONExporter creates a JSONExporter that writes indented JSON to w.
func NewJSONExporter(w io.Writer) *JSONExporter {
	return &JSONExporter{w: w}
}

// Export writes the result as indented JSON.
func (e *JSONExporter) Export(r *measurement.TrackerResult) error {
	enc := json.NewEncoder(e.w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// JSONFileExporter writes the result as indented JSON to a new file at path.
type JSONFileExporter struct {
	path string
}

// NewJSONFileExporter creates a JSONFileExporter that writes to path.
func NewJSONFileExporter(path string) *JSONFileExporter {
	return &JSONFileExporter{path: path}
}

// Export creates the file, writes indented JSON, and closes it.
// Close is called explicitly (not deferred) so flush errors are not discarded.
func (e *JSONFileExporter) Export(r *measurement.TrackerResult) error {
	f, err := os.Create(e.path)
	if err != nil {
		return fmt.Errorf("create %s: %w", e.path, err)
	}
	writeErr := NewJSONExporter(f).Export(r)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return fmt.Errorf("close %s: %w", e.path, closeErr)
	}
	return nil
}

// SummaryExporter writes a human-readable summary to the configured writer.
type SummaryExporter struct {
	w io.Writer
}

// NewSummaryExporter creates a SummaryExporter that writes to w.
func NewSummaryExporter(w io.Writer) *SummaryExporter {
	return &SummaryExporter{w: w}
}

// Export writes a human-readable summary of the result.
func (e *SummaryExporter) Export(r *measurement.TrackerResult) error {
	ew := &errWriter{w: e.w}
	ew.write("=== Test: %s (run: %s) ===\n", r.TestName, r.RunID)
	ew.write("Grove image:      %s\n", r.GroveImage)
	ew.write("Namespace:        %s\n", r.Namespace)
	ew.write("PCS count:        %d\n", r.PCSCount)
	if r.K8sClient != nil {
		ew.write("K8s client:       QPS=%.0f Burst=%d\n", r.K8sClient.QPS, r.K8sClient.Burst)
	}
	if r.ControllerMaxReconcile != nil {
		ew.write("Max reconcile:    pcs=%d pcsg=%d pclq=%d\n",
			r.ControllerMaxReconcile.PodCliqueSet,
			r.ControllerMaxReconcile.PodCliqueScalingGroup,
			r.ControllerMaxReconcile.PodClique)
	}
	ew.write("Total test time:  %.3fs\n", r.TestDurationSeconds)
	ew.write("Timeline:\n")

	for _, phase := range r.Phases {
		ew.write("  Phase: %s (started +%.3fs)\n", phase.Name, phase.DurationFromTestStart)
		for _, milestone := range phase.Milestones {
			ew.write("    %s  +%.3fs\n", milestone.Name, milestone.DurationFromPhaseStart)
		}
	}

	return ew.err
}

// errWriter wraps an io.Writer and captures the first error,
// allowing sequential writes without per-call error checks.
type errWriter struct {
	w   io.Writer
	err error
}

// write writes a formatted string to the underlying writer,
// short-circuiting if a previous write already failed.
func (ew *errWriter) write(format string, args ...any) {
	if ew.err != nil {
		return
	}
	_, ew.err = fmt.Fprintf(ew.w, format, args...)
}
