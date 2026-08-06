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

// Package webhook sets up Grove's production admission webhooks for tests.
package webhook

import (
	"fmt"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	schedulerregistry "github.com/ai-dynamo/grove/operator/internal/scheduler/registry"
	internalwebhook "github.com/ai-dynamo/grove/operator/internal/webhook"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Setup registers Grove's production admission webhooks with mgr.
// operatorCfg must be defaulted and validated like the production operator
// configuration before calling Setup.
func Setup(mgr ctrl.Manager, operatorCfg *configv1alpha1.OperatorConfiguration) error {
	if operatorCfg == nil {
		return fmt.Errorf("operator configuration must not be nil")
	}

	directClient, err := client.New(mgr.GetConfig(), client.Options{Scheme: mgr.GetScheme()})
	if err != nil {
		return fmt.Errorf("failed to create direct client: %w", err)
	}
	schedRegistry, err := schedulerregistry.New(
		mgr.GetClient(),
		directClient,
		mgr.GetScheme(),
		mgr.GetEventRecorderFor("scheduler-backend"),
		operatorCfg.Scheduler,
	)
	if err != nil {
		return fmt.Errorf("failed to initialize scheduler backends: %w", err)
	}

	return internalwebhook.Register(mgr, operatorCfg, schedRegistry)
}
