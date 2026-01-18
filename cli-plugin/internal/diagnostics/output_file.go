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
	"os"
	"path/filepath"
	"strings"
)

// FileOutput implements DiagnosticOutput for file-based output.
// This is used by the arborist CLI to write diagnostics to separate files.
type FileOutput struct {
	// outputDir is the directory where diagnostic files are written
	outputDir string

	// currentFile is the current file being written to
	currentFile *os.File

	// currentFileName is the name of the current file
	currentFileName string

	// summaryFile is the summary file that contains an overview
	summaryFile *os.File

	// yamlFiles tracks open YAML files by resource type
	yamlFiles map[string]*os.File

	// printToStdout also prints progress to stdout
	printToStdout bool
}

// NewFileOutput creates a new FileOutput that writes to the given directory.
// If printToStdout is true, progress messages are also printed to stdout.
func NewFileOutput(outputDir string, printToStdout bool) (*FileOutput, error) {
	// Create the output directory if it doesn't exist
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output directory %s: %w", outputDir, err)
	}

	// Create summary file
	summaryPath := filepath.Join(outputDir, "summary.txt")
	summaryFile, err := os.Create(summaryPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create summary file: %w", err)
	}

	return &FileOutput{
		outputDir:     outputDir,
		summaryFile:   summaryFile,
		yamlFiles:     make(map[string]*os.File),
		printToStdout: printToStdout,
	}, nil
}

// WriteSection writes a section header
func (f *FileOutput) WriteSection(title string) error {
	header := fmt.Sprintf("================================================================================\n=== %s ===\n================================================================================\n", title)

	// Write to summary
	if _, err := f.summaryFile.WriteString(header); err != nil {
		return err
	}

	if f.printToStdout {
		fmt.Print(header)
	}

	// Determine the appropriate file for this section and switch BEFORE writing header
	fileName := sectionToFileName(title)
	if fileName != "" {
		if err := f.switchToFile(fileName); err != nil {
			return err
		}
		// Write header to the NEW file (after switching)
		if f.currentFile != nil {
			if _, err := f.currentFile.WriteString(header); err != nil {
				return err
			}
		}
	}

	return nil
}

// WriteSubSection writes a subsection header
func (f *FileOutput) WriteSubSection(title string) error {
	header := fmt.Sprintf("--------------------------------------------------------------------------------\n--- %s ---\n--------------------------------------------------------------------------------\n", title)

	if f.currentFile != nil {
		if _, err := f.currentFile.WriteString(header); err != nil {
			return err
		}
	}

	// Also write to summary
	if _, err := fmt.Fprintf(f.summaryFile, "  - %s\n", title); err != nil {
		return err
	}

	return nil
}

// WriteLine writes a single line of text
func (f *FileOutput) WriteLine(line string) error {
	lineWithNewline := line + "\n"

	if f.currentFile != nil {
		if _, err := f.currentFile.WriteString(lineWithNewline); err != nil {
			return err
		}
	}

	return nil
}

// WriteLinef writes a formatted line of text
func (f *FileOutput) WriteLinef(format string, args ...any) error {
	line := fmt.Sprintf(format, args...)

	// Print INFO messages to stdout for progress feedback
	if f.printToStdout && (strings.HasPrefix(line, "[INFO]") || strings.HasPrefix(line, "[ERROR]")) {
		fmt.Println(line)
	}

	return f.WriteLine(line)
}

// WriteYAML writes YAML content for a resource
func (f *FileOutput) WriteYAML(resourceType, resourceName string, yamlContent []byte) error {
	// Get or create the YAML file for this resource type
	yamlFile, err := f.getYAMLFile(resourceType)
	if err != nil {
		return err
	}

	// Write separator and resource name
	separator := fmt.Sprintf("---\n# %s: %s\n", resourceType, resourceName)
	if _, err := yamlFile.WriteString(separator); err != nil {
		return err
	}

	// Write YAML content
	if _, err := yamlFile.Write(yamlContent); err != nil {
		return err
	}

	// Ensure there's a newline at the end
	if len(yamlContent) > 0 && yamlContent[len(yamlContent)-1] != '\n' {
		if _, err := yamlFile.WriteString("\n"); err != nil {
			return err
		}
	}

	return nil
}

// WriteTable writes tabular data
func (f *FileOutput) WriteTable(headers []string, rows [][]string) error {
	if f.currentFile == nil {
		return nil
	}

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
	formatStr := strings.Join(formats, " ") + "\n"

	// Write header
	headerArgs := make([]any, len(headers))
	for i, h := range headers {
		headerArgs[i] = h
	}
	if _, err := fmt.Fprintf(f.currentFile, formatStr, headerArgs...); err != nil {
		return err
	}

	// Write separator
	separatorParts := make([]string, len(widths))
	for i, w := range widths {
		separatorParts[i] = strings.Repeat("-", w)
	}
	if _, err := f.currentFile.WriteString(strings.Join(separatorParts, " ") + "\n"); err != nil {
		return err
	}

	// Write rows
	for _, row := range rows {
		rowArgs := make([]any, len(headers))
		for i := range headers {
			if i < len(row) {
				rowArgs[i] = row[i]
			} else {
				rowArgs[i] = ""
			}
		}
		if _, err := fmt.Fprintf(f.currentFile, formatStr, rowArgs...); err != nil {
			return err
		}
	}

	return nil
}

// Flush closes all open files
func (f *FileOutput) Flush() error {
	var errs []error

	// Close current file
	if f.currentFile != nil {
		if err := f.currentFile.Close(); err != nil {
			errs = append(errs, err)
		}
		f.currentFile = nil
	}

	// Close all YAML files
	for name, file := range f.yamlFiles {
		if err := file.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close %s: %w", name, err))
		}
	}
	f.yamlFiles = make(map[string]*os.File)

	// Close summary file
	if f.summaryFile != nil {
		if err := f.summaryFile.Close(); err != nil {
			errs = append(errs, err)
		}
		f.summaryFile = nil
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing files: %v", errs)
	}

	if f.printToStdout {
		fmt.Printf("\nDiagnostics written to: %s\n", f.outputDir)
	}

	return nil
}

// switchToFile switches to writing to a new file
func (f *FileOutput) switchToFile(fileName string) error {
	// Close current file if different
	if f.currentFileName == fileName {
		return nil
	}

	if f.currentFile != nil {
		if err := f.currentFile.Close(); err != nil {
			return err
		}
	}

	// Open new file
	filePath := filepath.Join(f.outputDir, fileName)
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", filePath, err)
	}

	f.currentFile = file
	f.currentFileName = fileName

	return nil
}

// getYAMLFile gets or creates a YAML file for the given resource type
func (f *FileOutput) getYAMLFile(resourceType string) (*os.File, error) {
	if file, ok := f.yamlFiles[resourceType]; ok {
		return file, nil
	}

	fileName := resourceType + ".yaml"
	filePath := filepath.Join(f.outputDir, fileName)
	file, err := os.Create(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create YAML file %s: %w", filePath, err)
	}

	f.yamlFiles[resourceType] = file
	return file, nil
}

// sectionToFileName maps section titles to file names
func sectionToFileName(title string) string {
	switch {
	case strings.Contains(title, "OPERATOR LOGS"):
		return "operator-logs.txt"
	case strings.Contains(title, "GROVE RESOURCES"):
		return "" // YAML files are handled separately
	case strings.Contains(title, "POD DETAILS"):
		return "pods.txt"
	case strings.Contains(title, "KUBERNETES EVENTS"):
		return "events.txt"
	default:
		return ""
	}
}

// Ensure FileOutput implements DiagnosticOutput
var _ DiagnosticOutput = (*FileOutput)(nil)
