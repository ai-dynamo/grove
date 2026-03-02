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

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend/kaischeduler"
	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend/kube"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Compile-time checks that backend implementations satisfy SchedBackend.
var (
	_ SchedBackend = (*kaischeduler.Backend)(nil)
	_ SchedBackend = (*kube.Backend)(nil)
)

// newBackendForProfile creates and initializes a SchedBackend for the given profile.
// Add new scheduler backends by extending this switch (no global registry).
func newBackendForProfile(cl client.Client, scheme *runtime.Scheme, rec record.EventRecorder, p configv1alpha1.SchedulerProfile) (SchedBackend, error) {
	switch p.Name {
	case configv1alpha1.SchedulerNameKube:
		b := kube.New(cl, scheme, rec, p)
		if err := b.Init(); err != nil {
			return nil, err
		}
		return b, nil
	case configv1alpha1.SchedulerNameKai:
		b := kaischeduler.New(cl, scheme, rec, p)
		if err := b.Init(); err != nil {
			return nil, err
		}
		return b, nil
	default:
		return nil, fmt.Errorf("scheduler profile %q is not supported", p.Name)
	}
}

var (
	backends       map[string]SchedBackend
	defaultBackend SchedBackend
)

// Initialize creates and registers backend instances for each profile in config.Profiles.
// Defaults are applied to config so that kube-scheduler is always present; only backends
// named in config.Profiles are started. Called once during operator startup before controllers start.
func Initialize(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, cfg configv1alpha1.SchedulerConfiguration) error {
	backends = make(map[string]SchedBackend)

	// New and init each backend from cfg.Profiles (order follows config; duplicate name overwrites).
	for _, p := range cfg.Profiles {
		backend, err := newBackendForProfile(client, scheme, eventRecorder, p)
		if err != nil {
			return fmt.Errorf("failed to initialize %s backend: %w", p.Name, err)
		}
		backends[backend.Name()] = backend
		if p.Default {
			defaultBackend = backend
		}
	}
	return nil
}

// Get returns the backend for the given name. default-scheduler is always available; other backends return nil if not enabled via a profile.
func Get(name string) SchedBackend {
	if name == "" {
		return defaultBackend
	}
	return backends[name]
}

// GetDefault returns the backend designated as default in OperatorConfiguration (the profile with default: true; if none, default-scheduler).
func GetDefault() SchedBackend {
	return defaultBackend
}
