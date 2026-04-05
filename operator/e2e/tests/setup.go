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
	"fmt"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/utils"
	v1 "k8s.io/api/core/v1"
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
}

const (
	// DefaultPollTimeout is the timeout for most polling conditions
	DefaultPollTimeout = 4 * time.Minute
	// DefaultPollInterval is the interval for most polling conditions
	DefaultPollInterval = 5 * time.Second

	// scaleTestPollInterval defines the interval at which polling occurs during scale tests, set to 2 seconds.
	scaleTestPollInterval = 2 * time.Second
	// scaleTestTimeout defines the timeout for scale tests, set to 15 minutes.
	scaleTestTimeout = 15 * time.Minute

	// Grove label keys
	LabelPodClique             = "grove.io/podclique"
	LabelPodCliqueScalingGroup = "grove.io/podcliquescalinggroup"
)

// assertPodsOnDistinctNodes asserts that the pods are scheduled on distinct nodes and fails the test if not.
func assertPodsOnDistinctNodes(t *testing.T, pods []v1.Pod) {
	t.Helper()

	assignedNodes := make(map[string]string, len(pods))
	for _, pod := range pods {
		nodeName := pod.Spec.NodeName
		if nodeName == "" {
			t.Fatalf("Pod %s is running but has no assigned node", pod.Name)
		}
		if existingPod, exists := assignedNodes[nodeName]; exists {
			t.Fatalf("Pods %s and %s are scheduled on the same node %s; expected unique nodes", existingPod, pod.Name, nodeName)
		}
		assignedNodes[nodeName] = pod.Name
	}
}

// WorkloadConfig defines configuration for deploying and verifying a workload.
type WorkloadConfig struct {
	Name         string
	YAMLPath     string
	Namespace    string
	ExpectedPods int
}

// GetLabelSelector returns the label selector calculated from the workload name.
// The label selector follows the pattern: "app.kubernetes.io/part-of=<name>"
func (w WorkloadConfig) GetLabelSelector() string {
	return fmt.Sprintf("app.kubernetes.io/part-of=%s", w.Name)
}

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
