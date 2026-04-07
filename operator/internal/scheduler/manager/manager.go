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

package manager

import (
	"fmt"
	"maps"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/kai"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/kube"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// newBackendForProfile creates and initializes a Backend for the given profile.
// Add new scheduler backends by extending this switch (no global registry).
func newBackendForProfile(cl client.Client, scheme *runtime.Scheme, rec record.EventRecorder, p configv1alpha1.SchedulerProfile) (scheduler.Backend, error) {
	switch p.Name {
	case configv1alpha1.SchedulerNameKube:
		b := kube.New(cl, scheme, rec, p)
		if err := b.Init(); err != nil {
			return nil, err
		}
		return b, nil
	case configv1alpha1.SchedulerNameKai:
		b := kai.New(cl, scheme, rec, p)
		if err := b.Init(); err != nil {
			return nil, err
		}
		return b, nil
	default:
		return nil, fmt.Errorf("scheduler profile %q is not supported", p.Name)
	}
}

// registry is the concrete implementation of scheduler.Registry.
type registry struct {
	backends       map[string]scheduler.Backend
	defaultBackend scheduler.Backend
}

func (r *registry) Get(name string) scheduler.Backend {
	return r.backends[name]
}

func (r *registry) GetDefault() scheduler.Backend {
	return r.defaultBackend
}

// NewRegistry creates a Registry by initializing backend instances for each
// profile in cfg.Profiles. Called once during operator startup.
func NewRegistry(cl client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, cfg configv1alpha1.SchedulerConfiguration) (scheduler.Registry, error) {
	reg := &registry{backends: make(map[string]scheduler.Backend)}
	for _, p := range cfg.Profiles {
		backend, err := newBackendForProfile(cl, scheme, eventRecorder, p)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize %s backend: %w", p.Name, err)
		}
		reg.backends[backend.Name()] = backend
		if string(p.Name) == cfg.DefaultProfileName {
			reg.defaultBackend = backend
		}
	}
	return reg, nil
}

// All returns all registered scheduler backends keyed by name.
func All() map[string]scheduler.Backend {
	result := make(map[string]scheduler.Backend)
	maps.Copy(result, backends)
	return result
}
