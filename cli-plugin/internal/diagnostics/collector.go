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
)

// CollectorFunc is a function that collects a specific type of diagnostic
type CollectorFunc func(dc *DiagnosticContext, output DiagnosticOutput) error

// Collector orchestrates the collection of all diagnostics
type Collector struct {
	collectors []namedCollector
}

type namedCollector struct {
	name string
	fn   CollectorFunc
}

// NewCollector creates a new Collector with the default set of diagnostic collectors
func NewCollector() *Collector {
	return &Collector{
		collectors: []namedCollector{
			{name: "Operator Logs", fn: CollectOperatorLogs},
			{name: "Grove Resources", fn: CollectGroveResources},
			{name: "Pod Details", fn: CollectPodDetails},
			{name: "Kubernetes Events", fn: CollectEvents},
		},
	}
}

// CollectAll runs all registered collectors and outputs diagnostics.
// It continues even if individual collectors fail, logging errors along the way.
// Returns an error only if there's a critical failure (e.g., output flush fails).
func (c *Collector) CollectAll(dc *DiagnosticContext, output DiagnosticOutput) error {
	if err := output.WriteSection("COLLECTING GROVE DIAGNOSTICS"); err != nil {
		return fmt.Errorf("failed to write header: %w", err)
	}

	if err := output.WriteLinef("Namespace: %s", dc.Namespace); err != nil {
		return fmt.Errorf("failed to write namespace info: %w", err)
	}
	if err := output.WriteLinef("Operator Namespace: %s", dc.OperatorNamespace); err != nil {
		return fmt.Errorf("failed to write operator namespace info: %w", err)
	}

	var collectionErrors []error

	for _, collector := range c.collectors {
		if err := c.safeRunCollector(collector, dc, output); err != nil {
			collectionErrors = append(collectionErrors, fmt.Errorf("%s: %w", collector.name, err))
			// Continue to next collector even if this one failed
		}
	}

	if err := output.WriteSection("END OF DIAGNOSTICS"); err != nil {
		return fmt.Errorf("failed to write footer: %w", err)
	}

	// Log any collection errors that occurred
	if len(collectionErrors) > 0 {
		_ = output.WriteLinef("Note: %d collector(s) encountered errors:", len(collectionErrors))
		for _, err := range collectionErrors {
			_ = output.WriteLinef("  - %v", err)
		}
	}

	// Flush output
	if err := output.Flush(); err != nil {
		return fmt.Errorf("failed to flush output: %w", err)
	}

	return nil
}

// safeRunCollector runs a collector with panic recovery
func (c *Collector) safeRunCollector(collector namedCollector, dc *DiagnosticContext, output DiagnosticOutput) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()

	return collector.fn(dc, output)
}

// CollectAllDiagnostics is a convenience function that creates a collector and runs all diagnostics
func CollectAllDiagnostics(dc *DiagnosticContext, output DiagnosticOutput) error {
	collector := NewCollector()
	return collector.CollectAll(dc, output)
}
