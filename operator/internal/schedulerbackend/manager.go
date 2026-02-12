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

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend/kai"
	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend/kube"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	// Global singleton backend instance
	globalBackend SchedulerBackend
	initOnce      sync.Once
)

// Initialize creates the global backend instance based on the scheduler configuration.
// This should be called once during operator startup.
// Supported scheduler names: "kai-scheduler", "default-scheduler"
func Initialize(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, cfg configv1alpha1.SchedulerConfiguration) error {
	var initErr error
	initOnce.Do(func() {
		// Create the appropriate backend based on scheduler name
		switch cfg.Name {
		case configv1alpha1.SchedulerNameKai:
			globalBackend = kai.New(client, scheme, eventRecorder, cfg)
		case configv1alpha1.SchedulerNameKube:
			globalBackend = kube.New(client, scheme, eventRecorder, cfg)
		default:
			initErr = fmt.Errorf("unsupported scheduler %q (supported: kai-scheduler, default-scheduler)", cfg.Name)
			return
		}

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
