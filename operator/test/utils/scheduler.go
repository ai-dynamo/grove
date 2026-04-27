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
	"strings"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

var _ scheduler.Registry = (*FakeSchedulerRegistry)(nil)

// FakeSchedulerRegistry is a test helper implementing scheduler.Registry.
// Set Backends and DefaultBackend fields directly for the desired test scenario.
type FakeSchedulerRegistry struct {
	Backends       map[string]scheduler.Backend
	DefaultBackend string
}

// Get returns the backend registered under name, or nil if not found.
func (r *FakeSchedulerRegistry) Get(name string) scheduler.Backend {
	if len(strings.TrimSpace(name)) == 0 {
		return r.GetDefault()
	}
	return r.Backends[name]
}

// GetDefault returns the default backend.
func (r *FakeSchedulerRegistry) GetDefault() scheduler.Backend {
	return r.Backends[r.DefaultBackend]
}

// All returns all registered scheduler backends keyed by name.
func (r *FakeSchedulerRegistry) All() map[string]scheduler.Backend {
	return r.Backends
}

// FakeSchedulerBackend is a minimal scheduler.Backend implementation for unit tests.
type FakeSchedulerBackend struct{ name string }

// NewFakeSchedulerBackend creates a new instance of FakeSchedulerBackend.
func NewFakeSchedulerBackend(name string) scheduler.Backend { return &FakeSchedulerBackend{name: name} }

// Name returns the backend name.
func (s *FakeSchedulerBackend) Name() string { return s.name }

// Init is a no-op for the fake backend.
func (s *FakeSchedulerBackend) Init() error { return nil }

// SyncPodGang is a no-op for the fake backend.
func (s *FakeSchedulerBackend) SyncPodGang(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	return nil
}

// OnPodGangDelete is a no-op for the fake backend.
func (s *FakeSchedulerBackend) OnPodGangDelete(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	return nil
}

// PreparePod is a no-op for the fake backend.
func (s *FakeSchedulerBackend) PreparePod(_ *corev1.Pod) {}

// ValidatePodCliqueSet is a no-op for the fake backend.
func (s *FakeSchedulerBackend) ValidatePodCliqueSet(_ context.Context, _ *grovecorev1alpha1.PodCliqueSet) error {
	return nil
}
