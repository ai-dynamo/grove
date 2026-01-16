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

package aic

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Parser handles parsing of AIConfigurator output.
type Parser struct{}

// NewParser creates a new AIConfigurator output parser.
func NewParser() *Parser {
	return &Parser{}
}

// Parse parses the AIConfigurator output and returns the result.
// Supports both JSON output and structured text output.
func (p *Parser) Parse(output string) (*GeneratorResult, error) {
	output = strings.TrimSpace(output)

	// Try JSON parsing first
	if strings.HasPrefix(output, "{") || strings.HasPrefix(output, "[") {
		return p.parseJSON(output)
	}

	// Fall back to text parsing
	return p.parseText(output)
}

// parseJSON parses JSON-formatted AIConfigurator output.
func (p *Parser) parseJSON(output string) (*GeneratorResult, error) {
	// Try parsing as AIConfiguratorOutput structure
	var aicOutput aicJSONOutput
	if err := json.Unmarshal([]byte(output), &aicOutput); err != nil {
		return nil, fmt.Errorf("failed to parse JSON output: %w", err)
	}

	result := &GeneratorResult{
		Plans: make([]GeneratorPlan, 0, len(aicOutput.Plans)),
	}

	for _, jp := range aicOutput.Plans {
		plan := GeneratorPlan{
			Mode:              jp.Mode,
			ModeName:          jp.ModeName,
			PrefillWorkers:    jp.PrefillWorkers,
			PrefillTP:         jp.PrefillTP,
			PrefillPP:         jp.PrefillPP,
			DecodeWorkers:     jp.DecodeWorkers,
			DecodeTP:          jp.DecodeTP,
			DecodePP:          jp.DecodePP,
			AggregatedWorkers: jp.AggregatedWorkers,
			AggregatedTP:      jp.AggregatedTP,
			AggregatedPP:      jp.AggregatedPP,
			TotalGPUUsage:     jp.TotalGPUUsage,
			Throughput:        jp.Throughput,
			TTFT:              jp.TTFT,
			TPOT:              jp.TPOT,
		}
		result.Plans = append(result.Plans, plan)
	}

	return result, nil
}

// aicJSONOutput represents the JSON output structure from AIConfigurator.
type aicJSONOutput struct {
	Plans []aicJSONPlan `json:"plans"`
}

// aicJSONPlan represents a single plan in JSON format.
type aicJSONPlan struct {
	Mode              string  `json:"mode"`
	ModeName          string  `json:"mode_name"`
	PrefillWorkers    int     `json:"prefill_workers"`
	PrefillTP         int     `json:"prefill_tp"`
	PrefillPP         int     `json:"prefill_pp"`
	DecodeWorkers     int     `json:"decode_workers"`
	DecodeTP          int     `json:"decode_tp"`
	DecodePP          int     `json:"decode_pp"`
	AggregatedWorkers int     `json:"aggregated_workers"`
	AggregatedTP      int     `json:"aggregated_tp"`
	AggregatedPP      int     `json:"aggregated_pp"`
	TotalGPUUsage     int     `json:"total_gpu_usage"`
	Throughput        float64 `json:"throughput"`
	TTFT              float64 `json:"ttft"`
	TPOT              float64 `json:"tpot"`
}

// parseText parses text-formatted AIConfigurator output.
func (p *Parser) parseText(output string) (*GeneratorResult, error) {
	result := &GeneratorResult{
		Plans: []GeneratorPlan{},
	}

	// Split output into plan sections
	sections := p.splitIntoPlanSections(output)

	for _, section := range sections {
		plan, err := p.parsePlanSection(section)
		if err != nil {
			continue // Skip malformed sections
		}
		result.Plans = append(result.Plans, plan)
	}

	if len(result.Plans) == 0 {
		return nil, fmt.Errorf("no valid plans found in output")
	}

	return result, nil
}

// splitIntoPlanSections splits the output into individual plan sections.
func (p *Parser) splitIntoPlanSections(output string) []string {
	// Look for "Plan N:" or similar markers
	planRegex := regexp.MustCompile(`(?m)^Plan\s+\d+:`)
	indices := planRegex.FindAllStringIndex(output, -1)

	if len(indices) == 0 {
		// No plan markers found, treat entire output as single section
		return []string{output}
	}

	sections := make([]string, 0, len(indices))
	for i, idx := range indices {
		start := idx[0]
		end := len(output)
		if i+1 < len(indices) {
			end = indices[i+1][0]
		}
		sections = append(sections, output[start:end])
	}

	return sections
}

// parsePlanSection parses a single plan section from text output.
func (p *Parser) parsePlanSection(section string) (GeneratorPlan, error) {
	plan := GeneratorPlan{}

	// Extract mode name from the first line
	modeRegex := regexp.MustCompile(`Plan\s+\d+:\s*(.+)`)
	if matches := modeRegex.FindStringSubmatch(section); len(matches) > 1 {
		plan.ModeName = strings.TrimSpace(matches[1])
		// Derive mode from mode name
		if strings.Contains(strings.ToLower(plan.ModeName), "disagg") {
			plan.Mode = "disaggregated"
		} else {
			plan.Mode = "aggregated"
		}
	}

	// Extract prefill workers
	prefillRegex := regexp.MustCompile(`Prefill Workers?:\s*(\d+)\s*\(tp(\d+)`)
	if matches := prefillRegex.FindStringSubmatch(section); len(matches) > 2 {
		plan.PrefillWorkers, _ = strconv.Atoi(matches[1])
		plan.PrefillTP, _ = strconv.Atoi(matches[2])
		plan.PrefillPP = 1
	}

	// Extract decode workers
	decodeRegex := regexp.MustCompile(`Decode Workers?:\s*(\d+)\s*\(tp(\d+)`)
	if matches := decodeRegex.FindStringSubmatch(section); len(matches) > 2 {
		plan.DecodeWorkers, _ = strconv.Atoi(matches[1])
		plan.DecodeTP, _ = strconv.Atoi(matches[2])
		plan.DecodePP = 1
	}

	// Extract aggregated workers (for non-disaggregated modes)
	workersRegex := regexp.MustCompile(`Workers?:\s*(\d+)\s*\(tp(\d+)`)
	if plan.PrefillWorkers == 0 && plan.DecodeWorkers == 0 {
		if matches := workersRegex.FindStringSubmatch(section); len(matches) > 2 {
			plan.AggregatedWorkers, _ = strconv.Atoi(matches[1])
			plan.AggregatedTP, _ = strconv.Atoi(matches[2])
			plan.AggregatedPP = 1
		}
	}

	// Extract total GPU usage
	gpuRegex := regexp.MustCompile(`Total GPU Usage:\s*(\d+)`)
	if matches := gpuRegex.FindStringSubmatch(section); len(matches) > 1 {
		plan.TotalGPUUsage, _ = strconv.Atoi(matches[1])
	}

	// Extract throughput
	throughputRegex := regexp.MustCompile(`Throughput:\s*([\d.]+)`)
	if matches := throughputRegex.FindStringSubmatch(section); len(matches) > 1 {
		plan.Throughput, _ = strconv.ParseFloat(matches[1], 64)
	}

	// Extract TTFT
	ttftRegex := regexp.MustCompile(`TTFT:\s*([\d.]+)`)
	if matches := ttftRegex.FindStringSubmatch(section); len(matches) > 1 {
		plan.TTFT, _ = strconv.ParseFloat(matches[1], 64)
	}

	// Extract TPOT
	tpotRegex := regexp.MustCompile(`TPOT:\s*([\d.]+)`)
	if matches := tpotRegex.FindStringSubmatch(section); len(matches) > 1 {
		plan.TPOT, _ = strconv.ParseFloat(matches[1], 64)
	}

	return plan, nil
}
