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

package pcs

import (
	"fmt"
	"log/slog"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	"github.com/ai-dynamo/grove/operator/internal/webhook/admission/pcs/defaulting"
	"github.com/ai-dynamo/grove/operator/internal/webhook/admission/pcs/validation"

	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// Register registers the PodCliqueSet mutating and validating webhooks with the controller manager.
func Register(mgr manager.Manager, operatorCfg *configv1alpha1.OperatorConfiguration, schedRegistry scheduler.Registry) error {
	if operatorCfg == nil {
		return fmt.Errorf("operator configuration must not be nil")
	}
	defaultingWebhook := defaulting.NewHandler(mgr)
	slog.Info("Registering webhook with manager", "handler", defaulting.Name)
	if err := defaultingWebhook.RegisterWithManager(mgr); err != nil {
		return fmt.Errorf("failed adding %s webhook handler: %v", defaulting.Name, err)
	}
	validatingWebhook := validation.NewHandler(mgr, operatorCfg, schedRegistry)
	slog.Info("Registering webhook with manager", "handler", validation.Name)
	if err := validatingWebhook.RegisterWithManager(mgr); err != nil {
		return fmt.Errorf("failed adding %s webhook handler: %v", validation.Name, err)
	}
	return nil
}
