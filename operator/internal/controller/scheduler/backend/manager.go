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

package backend

import (
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// BackendFactory creates a backend instance
type BackendFactory func(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder) SchedulerBackend

// BackendManager manages all backend instances as singletons
// This ensures we only create one backend instance per scheduler, shared across all components
type BackendManager struct {
	client        client.Client
	scheme        *runtime.Scheme
	eventRecorder record.EventRecorder
	backends      map[string]SchedulerBackend
	factories     map[string]BackendFactory
	mu            sync.RWMutex
}

var (
	globalManager     *BackendManager
	globalManagerOnce sync.Once
	globalFactories   = make(map[string]BackendFactory)
	globalFactoriesMu sync.RWMutex
)

// RegisterBackendFactory registers a backend factory for a scheduler name
// This should be called in backend package init() functions
func RegisterBackendFactory(schedulerName string, factory BackendFactory) {
	globalFactoriesMu.Lock()
	defer globalFactoriesMu.Unlock()
	globalFactories[schedulerName] = factory
}

// InitializeGlobalManager initializes the global backend manager
// This should be called once during operator startup, after the manager is created
func InitializeGlobalManager(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder) {
	globalManagerOnce.Do(func() {
		globalFactoriesMu.RLock()
		defer globalFactoriesMu.RUnlock()

		// Copy factories to manager
		factories := make(map[string]BackendFactory, len(globalFactories))
		for name, factory := range globalFactories {
			factories[name] = factory
		}

		globalManager = &BackendManager{
			client:        client,
			scheme:        scheme,
			eventRecorder: eventRecorder,
			backends:      make(map[string]SchedulerBackend),
			factories:     factories,
		}
	})
}

// GetGlobalManager returns the global backend manager instance
// Returns error if not initialized
func GetGlobalManager() (*BackendManager, error) {
	if globalManager == nil {
		return nil, fmt.Errorf("backend manager not initialized, call InitializeGlobalManager first")
	}
	return globalManager, nil
}

// GetBackend returns a backend for the given scheduler name
// Creates the backend on first access and caches it for subsequent calls
func (m *BackendManager) GetBackend(schedulerName string) (SchedulerBackend, error) {
	// Normalize scheduler name
	if schedulerName == "" {
		schedulerName = "default-scheduler"
	}

	// Try read lock first for performance
	m.mu.RLock()
	if backend, exists := m.backends[schedulerName]; exists {
		m.mu.RUnlock()
		return backend, nil
	}
	m.mu.RUnlock()

	// Need to create backend, acquire write lock
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check in case another goroutine created it
	if backend, exists := m.backends[schedulerName]; exists {
		return backend, nil
	}

	// Create backend using registered factory
	factory, exists := m.factories[schedulerName]
	if !exists {
		return nil, fmt.Errorf("no backend factory registered for scheduler: %s", schedulerName)
	}

	backend := factory(m.client, m.scheme, m.eventRecorder)

	// Cache for future use
	m.backends[schedulerName] = backend
	return backend, nil
}

// GetAllBackends returns all backends that support the Matches() interface
// Used by Backend Controllers to get backends for PodGang reconciliation
func (m *BackendManager) GetAllBackends() []SchedulerBackend {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Return all unique backend types (not scheduler names)
	// We want one instance per backend type, not per scheduler name
	backendTypes := make(map[string]SchedulerBackend)

	// Ensure workload and kai backends are created
	workload, _ := m.getOrCreateBackendLocked("default-scheduler")
	if workload != nil {
		backendTypes["workload"] = workload
	}

	kai, _ := m.getOrCreateBackendLocked("kai-scheduler")
	if kai != nil {
		backendTypes["kai"] = kai
	}

	// Convert to slice
	backends := make([]SchedulerBackend, 0, len(backendTypes))
	for _, backend := range backendTypes {
		backends = append(backends, backend)
	}

	return backends
}

// getOrCreateBackendLocked creates a backend without locking (assumes caller holds lock)
func (m *BackendManager) getOrCreateBackendLocked(schedulerName string) (SchedulerBackend, error) {
	if backend, exists := m.backends[schedulerName]; exists {
		return backend, nil
	}

	factory, exists := m.factories[schedulerName]
	if !exists {
		return nil, fmt.Errorf("no backend factory registered for scheduler: %s", schedulerName)
	}

	backend := factory(m.client, m.scheme, m.eventRecorder)
	m.backends[schedulerName] = backend
	return backend, nil
}
