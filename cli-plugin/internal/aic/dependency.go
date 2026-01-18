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
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

const (
	// AIConfiguratorBinary is the name of the aiconfigurator CLI binary
	AIConfiguratorBinary = "aiconfigurator"

	// MinRequiredVersion is the minimum required version of aiconfigurator
	MinRequiredVersion = "0.5.0"
)

// CheckAIConfiguratorAvailable checks if aiconfigurator is installed and meets version requirements.
// Returns nil if available and compatible, error otherwise.
func CheckAIConfiguratorAvailable() error {
	// Check if aiconfigurator command exists in PATH
	if _, err := exec.LookPath(AIConfiguratorBinary); err != nil {
		return fmt.Errorf("aiconfigurator is not installed\n\n"+
			"Please install it using one of the following methods:\n"+
			"  pip install aiconfigurator\n"+
			"Or visit: https://github.com/ai-dynamo/aiconfigurator")
	}

	// Try to get version information
	versionCmd := exec.Command(AIConfiguratorBinary, "version")
	output, err := versionCmd.CombinedOutput()
	if err != nil {
		// Version command failed, but tool exists - continue with warning
		return nil
	}

	// Extract version number from output using regex
	// Expected format: "aiconfigurator 0.5.0" or just "0.5.0"
	outputStr := string(output)
	versionRegex := regexp.MustCompile(`(\d+\.\d+\.\d+)`)
	matches := versionRegex.FindStringSubmatch(outputStr)

	if len(matches) < 2 {
		// Could not parse version, but tool is available
		return nil
	}

	versionStr := matches[1]

	// Check if version >= MinRequiredVersion
	if err := checkMinVersion(versionStr, MinRequiredVersion); err != nil {
		return fmt.Errorf("aiconfigurator version %s is too old (minimum required: %s)\n\n"+
			"Please upgrade using:\n"+
			"  pip install --upgrade aiconfigurator", versionStr, MinRequiredVersion)
	}

	return nil
}

// GetAIConfiguratorVersion returns the installed aiconfigurator version, or empty string if not found.
func GetAIConfiguratorVersion() string {
	versionCmd := exec.Command(AIConfiguratorBinary, "version")
	output, err := versionCmd.CombinedOutput()
	if err != nil {
		return ""
	}

	versionRegex := regexp.MustCompile(`(\d+\.\d+\.\d+)`)
	matches := versionRegex.FindStringSubmatch(string(output))
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

// checkMinVersion checks if actual version >= required version.
// Both versions should be in format "x.y.z".
func checkMinVersion(actual, required string) error {
	actualParts := strings.Split(actual, ".")
	requiredParts := strings.Split(required, ".")

	if len(actualParts) != 3 || len(requiredParts) != 3 {
		return fmt.Errorf("invalid version format")
	}

	for i := 0; i < 3; i++ {
		actualNum, err := strconv.Atoi(actualParts[i])
		if err != nil {
			return fmt.Errorf("invalid version number: %s", actual)
		}

		requiredNum, err := strconv.Atoi(requiredParts[i])
		if err != nil {
			return fmt.Errorf("invalid version number: %s", required)
		}

		if actualNum > requiredNum {
			return nil // actual version is higher
		}
		if actualNum < requiredNum {
			return fmt.Errorf("version too old")
		}
		// If equal, continue to check next part
	}

	// All parts are equal, version is exactly the required version
	return nil
}
