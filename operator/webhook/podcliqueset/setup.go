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

// Package podcliqueset registers Grove's PodCliqueSet admission webhooks.
package podcliqueset

import (
	"fmt"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/kube"
	pcswebhook "github.com/ai-dynamo/grove/operator/internal/webhook/admission/pcs"

	ctrl "sigs.k8s.io/controller-runtime"
)

// Setup registers Grove's production PodCliqueSet mutating and validating
// admission webhooks with mgr, using the default Grove operator configuration.
func Setup(mgr ctrl.Manager) error {
	operatorCfg := &configv1alpha1.OperatorConfiguration{}
	configv1alpha1.SetObjectDefaults_OperatorConfiguration(operatorCfg)

	profile := operatorCfg.Scheduler.Profiles[0]
	backend := kube.New(mgr.GetClient(), mgr.GetScheme(), nil, profile)
	if err := backend.Init(mgr.GetClient()); err != nil {
		return fmt.Errorf("failed to initialize default scheduler backend: %w", err)
	}

	return pcswebhook.Register(mgr, operatorCfg, defaultSchedulerRegistry{backend: backend})
}

type defaultSchedulerRegistry struct {
	backend scheduler.Backend
}

func (r defaultSchedulerRegistry) Get(name string) scheduler.Backend {
	if name == r.backend.Name() {
		return r.backend
	}
	return nil
}

func (r defaultSchedulerRegistry) GetDefault() scheduler.Backend {
	return r.backend
}

func (r defaultSchedulerRegistry) GetOrDefault(name string) scheduler.Backend {
	if name == "" {
		return r.GetDefault()
	}
	return r.Get(name)
}

func (r defaultSchedulerRegistry) All() map[string]scheduler.Backend {
	return map[string]scheduler.Backend{r.backend.Name(): r.backend}
}

func (defaultSchedulerRegistry) AllTopologyAware() map[string]scheduler.TopologyAwareBackend {
	return nil
}
