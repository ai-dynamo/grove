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

package opts

import (
	"fmt"
	"strconv"
	"strings"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"

	"github.com/spf13/pflag"
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
	podCliques []string // PodClique names with their minAvailable replicas in format "name:count"
}

// RegisterFlags registers all the flags that are defined for the init container.
func (c *CLIOptions) RegisterFlags() {
	// --podcliques=<podclique-fqn>:<minAvailable-replicas>
	// --podcliques=podclique-a:3 --podcliques=podclique-b:4 and so on for each PodClique.
	pflag.StringArrayVarP(&c.podCliques, "podcliques", "p", nil, "podclique name and minAvailable replicas seperated by comma, repeated for each podclique")
}

// GetPodCliqueDependencies returns the PodClique information as a map with the minAvailable associated with each PodClique name.
func (c *CLIOptions) GetPodCliqueDependencies() (map[string]int, error) {
	podCliqueDependencies := make(map[string]int)

	// Parse each "name:count" pair into the dependencies map
	for _, pair := range c.podCliques {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}

		nameAndMinAvailable := strings.Split(pair, ":")
		if len(nameAndMinAvailable) != 2 {
			return nil, groveerr.New(errCodeInvalidInput, operationParseFlag, fmt.Sprintf("expected two values per podclique, found %d", len(nameAndMinAvailable)))
		}

		replicas, err := strconv.Atoi(strings.TrimSpace(nameAndMinAvailable[1]))
		if err != nil {
			return nil, groveerr.WrapError(err, errCodeInvalidInput, operationParseFlag, "failed to convert replicas to int")
		}

		if replicas <= 0 {
			return nil, groveerr.New(errCodeInvalidInput, operationParseFlag, fmt.Sprintf("replica count must be positive, got %d", replicas))
		}

		podCliqueName := strings.TrimSpace(nameAndMinAvailable[0])
		if podCliqueName == "" {
			return nil, groveerr.New(errCodeInvalidInput, operationParseFlag, "podclique name cannot be empty")
		}

		podCliqueDependencies[podCliqueName] = replicas
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
