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
	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend/common"
	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend/kaischeduler"
	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend/kube"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Compile-time checks that backend implementations satisfy the common interface.
var (
	_ common.SchedBackend = (*kaischeduler.Backend)(nil)
	_ common.SchedBackend = (*kube.Backend)(nil)
)

// backendFactory creates and initializes a scheduler backend from a profile.
type backendFactory func(client.Client, *runtime.Scheme, record.EventRecorder, configv1alpha1.SchedulerProfile) (common.SchedBackend, error)

// backendFactories maps each supported SchedulerName to its constructor. Add new backends here.
var backendFactories = map[configv1alpha1.SchedulerName]backendFactory{
	configv1alpha1.SchedulerNameKube: func(cl client.Client, scheme *runtime.Scheme, rec record.EventRecorder, p configv1alpha1.SchedulerProfile) (common.SchedBackend, error) {
		b := kube.New(cl, scheme, rec, p)
		if err := b.Init(); err != nil {
			return nil, err
		}
		return b, nil
	},
	configv1alpha1.SchedulerNameKai: func(cl client.Client, scheme *runtime.Scheme, rec record.EventRecorder, p configv1alpha1.SchedulerProfile) (common.SchedBackend, error) {
		b := kaischeduler.New(cl, scheme, rec, p)
		if err := b.Init(); err != nil {
			return nil, err
		}
		return b, nil
	},
}

var (
	backends       map[string]common.SchedBackend
	defaultBackend common.SchedBackend
	initOnce       sync.Once
)

// Initialize creates and registers backend instances for each profile in config.Profiles.
// Defaults are applied to config so that kube-scheduler is always present; only backends
// named in config.Profiles are started. Called once during operator startup before controllers start.
func Initialize(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, cfg configv1alpha1.SchedulerConfiguration) error {
	var initErr error
	initOnce.Do(func() {
		backends = make(map[string]common.SchedBackend)

		// New and init each backend from cfg.Profiles (order follows config; duplicate name overwrites).
		for _, p := range cfg.Profiles {
			factory, ok := backendFactories[p.Name]
			if !ok {
				initErr = fmt.Errorf("scheduler profile %q is not supported", p.Name)
				return
			}
			backend, err := factory(client, scheme, eventRecorder, p)
			if err != nil {
				initErr = fmt.Errorf("failed to initialize %s backend: %w", p.Name, err)
				return
			}
			backends[string(p.Name)] = backend
			if p.Default {
				defaultBackend = backend
			}
		}
	})
	return initErr
}

// Get returns the backend for the given name. default-scheduler is always available; other backends return nil if not enabled via a profile.
func Get(name string) common.SchedBackend {
	if name == "" {
		return defaultBackend
	}
	return backends[name]
}

// GetDefault returns the backend designated as default in OperatorConfiguration (the profile with default: true; if none, default-scheduler).
func GetDefault() common.SchedBackend {
	return defaultBackend
}
