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

var (
	backends       map[string]scheduler.Backend
	defaultBackend scheduler.Backend
)

// Initialize creates and registers backend instances for each profile in config.Profiles.
// Defaults are applied to config so that default-scheduler is always present; only backends
// named in config.Profiles are started. Called once during operator startup before controllers start.
func Initialize(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, cfg configv1alpha1.SchedulerConfiguration) error {
	backends = make(map[string]scheduler.Backend)

	// New and init each backend from cfg.Profiles (order follows config; duplicate name overwrites).
	for _, p := range cfg.Profiles {
		backend, err := newBackendForProfile(client, scheme, eventRecorder, p)
		if err != nil {
			return fmt.Errorf("failed to initialize %s backend: %w", p.Name, err)
		}
		backends[backend.Name()] = backend
		// It is assumed that if you reach here then default profile name is set. OperatorConfiguration scheduler config
		// validation will fail and will disallow starting Grove operator if default profile is not set.
		if string(p.Name) == cfg.DefaultProfileName {
			defaultBackend = backend
		}
	}
	return nil
}

// Get returns the backend for the given name. Empty string is valid and returns the default backend (e.g. when Pod.Spec.SchedulerName is unset).
// default-scheduler is always available; other backends return nil if not enabled via a profile.
func Get(name string) scheduler.Backend {
	if name == "" {
		return defaultBackend
	}
	return backends[name]
}

// GetDefault returns the backend designated as default in OperatorConfiguration (scheduler.defaultProfileName).
func GetDefault() scheduler.Backend {
	return defaultBackend
}

// All returns all registered scheduler backends keyed by name.
func All() map[string]scheduler.Backend {
	result := make(map[string]scheduler.Backend)
	maps.Copy(result, backends)
	return result
}
