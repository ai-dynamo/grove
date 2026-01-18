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
	"strings"
)

// Logger defines the interface for logging output.
// This allows the console output to work with different logging implementations.
type Logger interface {
	Info(msg string)
	Infof(format string, args ...any)
}

// ConsoleOutput implements DiagnosticOutput for console/logger output.
// This is used by e2e tests to output diagnostics to the test log.
type ConsoleOutput struct {
	logger Logger
}

// NewConsoleOutput creates a new ConsoleOutput with the given logger
func NewConsoleOutput(logger Logger) *ConsoleOutput {
	return &ConsoleOutput{
		logger: logger,
	}
}

// WriteSection writes a section header
func (c *ConsoleOutput) WriteSection(title string) error {
	c.logger.Info("================================================================================")
	c.logger.Infof("=== %s ===", title)
	c.logger.Info("================================================================================")
	return nil
}

// WriteSubSection writes a subsection header
func (c *ConsoleOutput) WriteSubSection(title string) error {
	c.logger.Info("--------------------------------------------------------------------------------")
	c.logger.Infof("--- %s ---", title)
	c.logger.Info("--------------------------------------------------------------------------------")
	return nil
}

// WriteLine writes a single line of text
func (c *ConsoleOutput) WriteLine(line string) error {
	c.logger.Info(line)
	return nil
}

// WriteLinef writes a formatted line of text
func (c *ConsoleOutput) WriteLinef(format string, args ...any) error {
	c.logger.Infof(format, args...)
	return nil
}

// WriteYAML writes YAML content for a resource
func (c *ConsoleOutput) WriteYAML(_, _ string, yamlContent []byte) error {
	// Print YAML line by line for better log formatting
	for _, line := range strings.Split(string(yamlContent), "\n") {
		c.logger.Info(line)
	}
	return nil
}

// WriteTable writes tabular data
func (c *ConsoleOutput) WriteTable(headers []string, rows [][]string) error {
	// Calculate column widths
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	// Build format string
	formats := make([]string, len(widths))
	for i, w := range widths {
		formats[i] = fmt.Sprintf("%%-%ds", w)
	}
	formatStr := strings.Join(formats, " ")

	// Print header
	headerArgs := make([]any, len(headers))
	for i, h := range headers {
		headerArgs[i] = h
	}
	c.logger.Infof(formatStr, headerArgs...)

	// Print separator
	separatorParts := make([]string, len(widths))
	for i, w := range widths {
		separatorParts[i] = strings.Repeat("-", w)
	}
	c.logger.Info(strings.Join(separatorParts, " "))

	// Print rows
	for _, row := range rows {
		rowArgs := make([]any, len(row))
		for i, cell := range row {
			rowArgs[i] = cell
		}
		// Pad row if needed
		for i := len(row); i < len(headers); i++ {
			rowArgs = append(rowArgs, "")
		}
		c.logger.Infof(formatStr, rowArgs...)
	}

	return nil
}

// Flush is a no-op for console output
func (c *ConsoleOutput) Flush() error {
	return nil
}

// Ensure ConsoleOutput implements DiagnosticOutput
var _ DiagnosticOutput = (*ConsoleOutput)(nil)
