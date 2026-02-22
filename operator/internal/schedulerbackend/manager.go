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
	backends       map[string]SchedulerBackend
	defaultBackend SchedulerBackend
	initOnce       sync.Once
)

// Initialize creates and registers backend instances: kube-scheduler (default-scheduler) is always
// registered; backends named in config.Profiles are also registered. Called once during operator
// startup before controllers start.
func Initialize(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, cfg configv1alpha1.SchedulerConfiguration) error {
	var initErr error
	initOnce.Do(func() {
		backends = make(map[string]SchedulerBackend)

		// Build profile config per backend name (last profile wins if duplicate)
		profileByName := make(map[configv1alpha1.SchedulerName]configv1alpha1.SchedulerProfile)
		for _, p := range cfg.Profiles {
			profileByName[p.Name] = p
		}

		// Kube backend is always available; create from profile if present (profile name "kube-scheduler"), else implicit default
		kubeProfile, hasKube := profileByName[configv1alpha1.SchedulerNameKube]
		if !hasKube {
			kubeProfile = configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKube, Default: true}
		}
		kubeBackend := kube.New(client, scheme, eventRecorder, kubeProfile)
		if err := kubeBackend.Init(); err != nil {
			initErr = fmt.Errorf("failed to initialize kube backend: %w", err)
			return
		}
		// Register under profile name and under pod schedulerName so Get("kube-scheduler") and Get("default-scheduler") both work
		backends[string(configv1alpha1.SchedulerNameKube)] = kubeBackend
		backends["default-scheduler"] = kubeBackend

		// Register kai backend only if it has a profile
		if kaiProfile, ok := profileByName[configv1alpha1.SchedulerNameKai]; ok {
			kaiBackend := kai.New(client, scheme, eventRecorder, kaiProfile)
			if err := kaiBackend.Init(); err != nil {
				initErr = fmt.Errorf("failed to initialize kai backend: %w", err)
				return
			}
			backends[string(configv1alpha1.SchedulerNameKai)] = kaiBackend
		}

		// Default: profile with Default=true, else kube
		for _, p := range cfg.Profiles {
			if p.Default {
				if b, ok := backends[string(p.Name)]; ok {
					defaultBackend = b
					break
				}
				// Default profile names a scheduler that is not registered (e.g. unsupported name)
				initErr = fmt.Errorf("default scheduler profile %q is not supported or not enabled", p.Name)
				return
			}
		}
		if defaultBackend == nil {
			defaultBackend = backends[string(configv1alpha1.SchedulerNameKube)]
		}
	})
	return initErr
}

// Get returns the backend for the given name. default-scheduler is always available; other backends return nil if not enabled via a profile.
func Get(name string) SchedulerBackend {
	if name == "" {
		return defaultBackend
	}
	return backends[name]
}

// GetDefault returns the backend designated as default in OperatorConfiguration (the profile with default: true; if none, default-scheduler).
func GetDefault() SchedulerBackend {
	return defaultBackend
}
