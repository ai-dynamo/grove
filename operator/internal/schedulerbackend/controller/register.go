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

package controller

import (
	"fmt"

	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NewReconciler creates a new BackendReconciler
func NewReconciler(mgr ctrl.Manager) (*BackendReconciler, error) {
	// Get the configured backend
	backend := schedulerbackend.Get()
	if backend == nil {
		return nil, fmt.Errorf("backend not initialized")
	}

	return &BackendReconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		Backend: backend,
	}, nil
}

// NewReconcilerWithBackend creates a new BackendReconciler with explicit dependencies
// Useful for testing
func NewReconcilerWithBackend(client client.Client, scheme *runtime.Scheme, backend schedulerbackend.SchedulerBackend) *BackendReconciler {
	return &BackendReconciler{
		Client:  client,
		Scheme:  scheme,
		Backend: backend,
	}
}
