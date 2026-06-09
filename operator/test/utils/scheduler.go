// /*
// Copyright 2026 The Grove Authors.
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

package utils

import (
	"context"
	"maps"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ scheduler.Registry = (*FakeSchedulerRegistry)(nil)

// FakeSchedulerRegistry is a test helper implementing scheduler.Registry.
// Set backends and defaultBackend fields directly for the desired test scenario.
type FakeSchedulerRegistry struct {
	Backends       map[string]scheduler.Backend
	DefaultBackend string
}

// NewDefaultFakeRegistry returns a FakeSchedulerRegistry pre-configured with the default kube scheduler backend.
func NewDefaultFakeRegistry() *FakeSchedulerRegistry {
	return &FakeSchedulerRegistry{
		Backends:       map[string]scheduler.Backend{"default-scheduler": &FakeSchedulerBackend{name: "default-scheduler"}},
		DefaultBackend: "default-scheduler",
	}
}

// Get returns the backend registered under name, or nil if not found.
func (r *FakeSchedulerRegistry) Get(name string) scheduler.Backend {
	return r.Backends[name]
}

// GetDefault returns the default backend.
func (r *FakeSchedulerRegistry) GetDefault() scheduler.Backend {
	return r.Backends[r.DefaultBackend]
}

// GetOrDefault returns the backend registered under name, or the default backend if name is empty.
func (r *FakeSchedulerRegistry) GetOrDefault(name string) scheduler.Backend {
	if name == "" {
		return r.GetDefault()
	}
	return r.Backends[name]
}

// All returns all registered scheduler backends keyed by name.
func (r *FakeSchedulerRegistry) All() map[string]scheduler.Backend {
	result := make(map[string]scheduler.Backend, len(r.Backends))
	maps.Copy(result, r.Backends)
	return result
}

// AllTopologyAware returns the subset of registered backends that implement TopologyAwareBackend, keyed by name.
func (r *FakeSchedulerRegistry) AllTopologyAware() map[string]scheduler.TopologyAwareBackend {
	result := make(map[string]scheduler.TopologyAwareBackend)
	for name, b := range r.Backends {
		if tas, ok := b.(scheduler.TopologyAwareBackend); ok {
			result[name] = tas
		}
	}
	return result
}

// FakeSchedulerBackend is a minimal scheduler.Backend implementation for unit tests.
type FakeSchedulerBackend struct{ name string }

// NewFakeSchedulerBackend creates a new instance of FakeSchedulerBackend.
func NewFakeSchedulerBackend(name string) scheduler.Backend { return &FakeSchedulerBackend{name: name} }

// Name returns the backend name.
func (s *FakeSchedulerBackend) Name() string { return s.name }

// Init is a no-op for the fake backend.
func (s *FakeSchedulerBackend) Init(_ client.Client) error { return nil }

// SyncPodGang is a no-op for the fake backend.
func (s *FakeSchedulerBackend) SyncPodGang(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	return nil
}

// PreparePod is a no-op for the fake backend.
func (s *FakeSchedulerBackend) PreparePod(_ *corev1.Pod) {}

// ValidatePodCliqueSet is a no-op for the fake backend.
func (s *FakeSchedulerBackend) ValidatePodCliqueSet(_ context.Context, _ *grovecorev1alpha1.PodCliqueSet) error {
	return nil
}

// NewFakeTopologyAwareBackend creates a FakeSchedulerBackend that also satisfies scheduler.TopologyAwareBackend.
func NewFakeTopologyAwareBackend(name string) *FakeTopologyAwareBackend {
	return &FakeTopologyAwareBackend{FakeSchedulerBackend: FakeSchedulerBackend{name: name}}
}

// FakeTopologyAwareBackend extends FakeSchedulerBackend with a no-op TopologyAwareBackend implementation.
type FakeTopologyAwareBackend struct {
	FakeSchedulerBackend
}

var _ scheduler.TopologyAwareBackend = (*FakeTopologyAwareBackend)(nil)

// TopologyGVR returns a zero-value GVR for the fake topology-aware backend.
func (s *FakeTopologyAwareBackend) TopologyGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{}
}

// TopologyResourceName returns an empty string for the fake topology-aware backend.
func (s *FakeTopologyAwareBackend) TopologyResourceName(_ *grovecorev1alpha1.ClusterTopologyBinding) string {
	return ""
}

// SyncTopology is a no-op for the fake topology-aware backend.
func (s *FakeTopologyAwareBackend) SyncTopology(_ context.Context, _ client.Client, _ *grovecorev1alpha1.ClusterTopologyBinding) error {
	return nil
}

// OnTopologyDelete is a no-op for the fake topology-aware backend.
func (s *FakeTopologyAwareBackend) OnTopologyDelete(_ context.Context, _ client.Client, _ *grovecorev1alpha1.ClusterTopologyBinding) error {
	return nil
}

// CheckTopologyDrift is a no-op for the fake topology-aware backend.
func (s *FakeTopologyAwareBackend) CheckTopologyDrift(_ context.Context, _ *grovecorev1alpha1.ClusterTopologyBinding, _ grovecorev1alpha1.SchedulerTopologyBinding) (bool, string, int64, error) {
	return true, "", 0, nil
}
