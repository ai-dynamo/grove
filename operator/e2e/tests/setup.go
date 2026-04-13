//go:build e2e

package tests

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

import (
	"encoding/json"
	"flag"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/utils"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
)

var (
	// Logger for the tests (exported for sub-packages)
	Logger *utils.Logger

	// TestImages are the Docker images to push to the test registry
	TestImages = []string{"busybox:latest"}
)

func init() {
	// Initialize klog flags and set them to suppress stderr output.
	// This prevents warning messages like "restartPolicy will be ignored" from appearing in test output.
	// Comment this out if you want to see the warnings, but they all seem harmless and noisy.
	klog.InitFlags(nil)
	if err := flag.Set("logtostderr", "false"); err != nil {
		panic("Failed to set logtostderr flag")
	}

	if err := flag.Set("alsologtostderr", "false"); err != nil {
		panic("Failed to set alsologtostderr flag")
	}

	// increase Logger verbosity for debugging
	Logger = utils.NewTestLogger(utils.InfoLevel)
	testctx.Logger = Logger
}

const (
	// scaleTestPollInterval defines the interval at which polling occurs during scale tests, set to 2 seconds.
	scaleTestPollInterval = 2 * time.Second
	// scaleTestTimeout defines the timeout for scale tests, set to 15 minutes.
	scaleTestTimeout = 15 * time.Minute

	// Grove label keys
	LabelPodClique             = "grove.io/podclique"
	LabelPodCliqueScalingGroup = "grove.io/podcliquescalinggroup"
)

// ConvertTypedToUnstructured converts a typed object to an unstructured object
func ConvertTypedToUnstructured(typed interface{}) (*unstructured.Unstructured, error) {
	data, err := json.Marshal(typed)
	if err != nil {
		return nil, err
	}
	var unstructuredMap map[string]interface{}
	err = json.Unmarshal(data, &unstructuredMap)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: unstructuredMap}, nil
}
