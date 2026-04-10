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

package registry

import (
	"fmt"
	"maps"
	"strings"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/kai"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/kube"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ensures at compile time that registry type implements scheduler.Registry interface.
var _ scheduler.Registry = (*registry)(nil)

// registry is the concrete implementation of scheduler.Registry.
type registry struct {
	backends       map[string]scheduler.Backend
	defaultBackend scheduler.Backend
}

// New creates a scheduler.Registry by initializing backend instances for each
// profile in cfg.Profiles.
// NOTE: This function should be called once during the lifecycle of the Grove operator.
func New(cl client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, cfg configv1alpha1.SchedulerConfiguration) (scheduler.Registry, error) {
	reg := &registry{backends: make(map[string]scheduler.Backend)}
	for _, p := range cfg.Profiles {
		backend, err := newSchedulerBackend(cl, scheme, eventRecorder, p)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize %s backend: %w", p.Name, err)
		}
		reg.backends[backend.Name()] = backend
		// It is assumed that scheduler configuration will be validated as part of OperatorConfiguration
		// validation to ensure that there is at most one default scheduler backend.
		if string(p.Name) == cfg.DefaultProfileName {
			reg.defaultBackend = backend
		}
	}
	return reg, nil
}

func (r *registry) Get(name string) scheduler.Backend {
	if len(strings.TrimSpace(name)) == 0 {
		return r.defaultBackend
	}
	return r.backends[name]
}

func (r *registry) GetDefault() scheduler.Backend {
	return r.defaultBackend
}

// newSchedulerBackend creates and initializes a Backend for the given profile.
// NOTE: For any newly supported backend, add a case for it in the switch statement.
func newSchedulerBackend(cl client.Client, scheme *runtime.Scheme, rec record.EventRecorder, p configv1alpha1.SchedulerProfile) (scheduler.Backend, error) {
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

// All returns all registered scheduler backends keyed by name.
func (r *registry) All() map[string]scheduler.Backend {
	result := make(map[string]scheduler.Backend)
	maps.Copy(result, r.backends)
	return result
}
