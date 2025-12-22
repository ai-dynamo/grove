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

package schedulerbackend

import (
	"fmt"
	"sync"

	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend/kai"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	// Global singleton backend instance
	globalBackend       SchedulerBackend
	globalSchedulerName string
	initOnce            sync.Once
)

// Initialize creates the global backend instance based on schedulerName
// This should be called once during operator startup
// Supported scheduler names: "kai-scheduler", "grove-scheduler"
// Future: "default-scheduler", "kube-scheduler", "volcano"
func Initialize(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, schedulerName string) error {
	var initErr error
	initOnce.Do(func() {
		// Default to "kai-scheduler" if not specified
		if schedulerName == "" {
			schedulerName = "kai-scheduler"
		}

		// Create the appropriate backend based on scheduler name
		switch schedulerName {
		case "kai-scheduler", "grove-scheduler":
			globalBackend = kai.New(client, scheme, eventRecorder, schedulerName)

		// Future backends - uncomment and implement as needed:
		// case "default-scheduler", "kube-scheduler":
		//     globalBackend = kube.New(client, scheme, eventRecorder, schedulerName)
		// case "volcano":
		//     globalBackend = volcano.New(client, scheme, eventRecorder, schedulerName)

		default:
			initErr = fmt.Errorf("unsupported scheduler %q (supported: kai-scheduler, grove-scheduler)", schedulerName)
			return
		}

		globalSchedulerName = schedulerName

		// Initialize the backend
		if err := globalBackend.Init(); err != nil {
			initErr = fmt.Errorf("failed to initialize backend: %w", err)
			globalBackend = nil
			return
		}
	})
	return initErr
}

// Get returns the global backend instance
// Returns nil if not initialized (caller should check)
func Get() SchedulerBackend {
	return globalBackend
}

// MustGet returns the global backend instance or panics if not initialized
// Use this only in contexts where initialization is guaranteed (e.g., after startup)
func MustGet() SchedulerBackend {
	if globalBackend == nil {
		panic("backend not initialized, call Initialize first")
	}
	return globalBackend
}

// GetSchedulerName returns the configured scheduler name
func GetSchedulerName() string {
	return globalSchedulerName
}

// IsInitialized returns true if the backend has been initialized
func IsInitialized() bool {
	return globalBackend != nil
}
