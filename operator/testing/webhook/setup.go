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

// Options configures the scheduler backends available to admission webhooks.
// The Kubernetes default scheduler is always enabled.
type Options struct {
	KAI     bool
	Volcano bool
	LPX     bool

	// Default selects the default scheduler backend. An empty value selects the
	// Kubernetes default scheduler.
	Default configv1alpha1.SchedulerName
}

// Setup registers Grove's production admission webhooks with mgr.
func Setup(mgr ctrl.Manager, options Options) error {
	operatorCfg, err := newOperatorConfiguration(options)
	if err != nil {
		return err
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

func newOperatorConfiguration(options Options) (*configv1alpha1.OperatorConfiguration, error) {
	profiles := []configv1alpha1.SchedulerProfile{{Name: configv1alpha1.SchedulerNameKube}}
	enabled := map[configv1alpha1.SchedulerName]bool{
		configv1alpha1.SchedulerNameKube: true,
	}
	enable := func(name configv1alpha1.SchedulerName, include bool) {
		if include {
			profiles = append(profiles, configv1alpha1.SchedulerProfile{Name: name})
			enabled[name] = true
		}
	}
	enable(configv1alpha1.SchedulerNameKai, options.KAI)
	enable(configv1alpha1.SchedulerNameVolcano, options.Volcano)
	enable(configv1alpha1.SchedulerNameLPX, options.LPX)

	defaultScheduler := options.Default
	if defaultScheduler == "" {
		defaultScheduler = configv1alpha1.SchedulerNameKube
	}
	if !enabled[defaultScheduler] {
		return nil, fmt.Errorf("default scheduler %q is not enabled", defaultScheduler)
	}

	operatorCfg := &configv1alpha1.OperatorConfiguration{
		Scheduler: configv1alpha1.SchedulerConfiguration{
			Profiles:           profiles,
			DefaultProfileName: string(defaultScheduler),
		},
	}
	configv1alpha1.SetObjectDefaults_OperatorConfiguration(operatorCfg)
	return operatorCfg, nil
}
