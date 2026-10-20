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

package opts

import (
	"fmt"
	"strconv"
	"strings"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/initc/internal"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"

	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
)

// Constants for error codes.
const (
	errCodeInvalidInput grovecorev1alpha1.ErrorCode = "ERR_INVALID_INPUT"
)

const (
	operationParseFlag = "OperationParseFlag"
)

// CLIOptions defines the configuration that is passed to the init container.
type CLIOptions struct {
	podCliques []string // PodClique names with their required pod counts in format "name:count[:conditionType]"
}

// RegisterFlags registers all the flags that are defined for the init container.
func (c *CLIOptions) RegisterFlags() {
	// --podcliques=<podclique-fqn>:<count>[:<conditionType>]
	// --podcliques=podclique-a:3 --podcliques=podclique-b:4:example.com/model-loaded and so on for each PodClique.
	// A missing conditionType means the Ready condition.
	pflag.StringArrayVarP(&c.podCliques, "podcliques", "p", nil, "podclique name, required pod count and optional pod condition type separated by colons, repeated for each podclique")
}

// GetPodCliqueDependencies returns the startup dependency on each parent PodClique, keyed by the PodClique name.
func (c *CLIOptions) GetPodCliqueDependencies() (map[string]internal.PodCliqueDependency, error) {
	podCliqueDependencies := make(map[string]internal.PodCliqueDependency)

	// Parse each "name:count[:conditionType]" value into the dependencies map
	for _, pair := range c.podCliques {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}

		parts := strings.Split(pair, ":")
		if len(parts) != 2 && len(parts) != 3 {
			return nil, groveerr.New(errCodeInvalidInput, operationParseFlag, fmt.Sprintf("expected two or three values per podclique, found %d", len(parts)))
		}

		replicas, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, groveerr.WrapError(err, errCodeInvalidInput, operationParseFlag, "failed to convert replicas to int")
		}

		if replicas <= 0 {
			return nil, groveerr.New(errCodeInvalidInput, operationParseFlag, fmt.Sprintf("replica count must be positive, got %d", replicas))
		}

		podCliqueName := strings.TrimSpace(parts[0])
		if podCliqueName == "" {
			return nil, groveerr.New(errCodeInvalidInput, operationParseFlag, "podclique name cannot be empty")
		}

		conditionType := corev1.PodReady
		if len(parts) == 3 {
			conditionType = corev1.PodConditionType(strings.TrimSpace(parts[2]))
			if conditionType == "" {
				return nil, groveerr.New(errCodeInvalidInput, operationParseFlag, "pod condition type cannot be empty")
			}
		}

		podCliqueDependencies[podCliqueName] = internal.PodCliqueDependency{MinReady: replicas, ConditionType: conditionType}
	}

	return podCliqueDependencies, nil
}

// InitializeCLIOptions parses the command line flags into CLIOptions.
func InitializeCLIOptions() (CLIOptions, error) {
	config := CLIOptions{
		podCliques: make([]string, 0),
	}
	config.RegisterFlags()
	pflag.Parse()
	return config, nil
}
