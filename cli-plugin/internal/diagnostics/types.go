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
	"context"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const (
	// LogBufferSize is the size of the buffer for reading logs from the operator
	LogBufferSize = 64 * 1024 // 64KB

	// OperatorLogLines is the number of log lines to capture from the operator.
	// Set to 2000 to ensure we capture logs from before the failure occurred,
	// not just the steady-state logs after the failure.
	OperatorLogLines = 2000

	// EventLookbackDuration is how far back to look for events
	EventLookbackDuration = 10 * time.Minute

	// DefaultOperatorNamespace is the default namespace where the Grove operator is deployed
	DefaultOperatorNamespace = "grove-system"

	// DefaultOperatorDeploymentPrefix is the prefix for the operator deployment name
	DefaultOperatorDeploymentPrefix = "grove-operator"
)

// DiagnosticContext provides the context and clients needed for collecting diagnostics.
// This is decoupled from the e2e TestContext to allow reuse in CLI tools.
type DiagnosticContext struct {
	// Ctx is the context for API calls
	Ctx context.Context

	// Clientset is the Kubernetes clientset for core API operations
	Clientset kubernetes.Interface

	// DynamicClient is the dynamic client for accessing CRDs
	DynamicClient dynamic.Interface

	// Namespace is the namespace to collect diagnostics from (for workloads)
	Namespace string

	// OperatorNamespace is the namespace where the Grove operator is deployed
	OperatorNamespace string

	// OperatorDeploymentPrefix is the prefix for the operator deployment name
	OperatorDeploymentPrefix string
}

// NewDiagnosticContext creates a new DiagnosticContext with sensible defaults
func NewDiagnosticContext(
	ctx context.Context,
	clientset kubernetes.Interface,
	dynamicClient dynamic.Interface,
	namespace string,
) *DiagnosticContext {
	return &DiagnosticContext{
		Ctx:                      ctx,
		Clientset:                clientset,
		DynamicClient:            dynamicClient,
		Namespace:                namespace,
		OperatorNamespace:        DefaultOperatorNamespace,
		OperatorDeploymentPrefix: DefaultOperatorDeploymentPrefix,
	}
}

// DiagnosticOutput defines the interface for outputting diagnostic information.
// Implementations can write to console (for e2e tests) or files (for CLI).
type DiagnosticOutput interface {
	// WriteSection writes a section header
	WriteSection(title string) error

	// WriteSubSection writes a subsection header
	WriteSubSection(title string) error

	// WriteLine writes a single line of text
	WriteLine(line string) error

	// WriteLinef writes a formatted line of text
	WriteLinef(format string, args ...any) error

	// WriteYAML writes YAML content for a resource
	// resourceType is the type of resource (e.g., "podcliquesets")
	// resourceName is the name of the specific resource
	// yamlContent is the YAML string to write
	WriteYAML(resourceType, resourceName string, yamlContent []byte) error

	// WriteTable writes tabular data
	// headers are the column headers
	// rows are the data rows
	WriteTable(headers []string, rows [][]string) error

	// Flush ensures all output is written (e.g., closes files)
	Flush() error
}

// GroveResourceType defines a Grove resource type for diagnostics
type GroveResourceType struct {
	// Name is the plural name of the resource (e.g., "PodCliqueSets")
	Name string

	// GVR is the GroupVersionResource for the resource
	GVR schema.GroupVersionResource

	// Singular is the singular uppercase name for display (e.g., "PODCLIQUESET")
	Singular string

	// Filename is the base filename for file output (e.g., "podcliquesets")
	Filename string
}

// GroveResourceTypes lists all Grove resource types to collect diagnostics for
var GroveResourceTypes = []GroveResourceType{
	{
		Name:     "PodCliqueSets",
		GVR:      schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"},
		Singular: "PODCLIQUESET",
		Filename: "podcliquesets",
	},
	{
		Name:     "PodCliques",
		GVR:      schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliques"},
		Singular: "PODCLIQUE",
		Filename: "podcliques",
	},
	{
		Name:     "PodCliqueScalingGroups",
		GVR:      schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquescalinggroups"},
		Singular: "PODCLIQUESCALINGGROUP",
		Filename: "podcliquescalinggroups",
	},
	{
		Name:     "PodGangs",
		GVR:      schema.GroupVersionResource{Group: "scheduler.grove.io", Version: "v1alpha1", Resource: "podgangs"},
		Singular: "PODGANG",
		Filename: "podgangs",
	},
}
