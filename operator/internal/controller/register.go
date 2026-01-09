// /*
// Copyright 2024 The Grove Authors.
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

package controller

import (
	"fmt"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/podclique"
	"github.com/ai-dynamo/grove/operator/internal/controller/podcliquescalinggroup"
	"github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset"
	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend"
	backendcontroller "github.com/ai-dynamo/grove/operator/internal/schedulerbackend/controller"

	ctrl "sigs.k8s.io/controller-runtime"
)

// RegisterControllers registers all controllers with the manager.
func RegisterControllers(mgr ctrl.Manager, config configv1alpha1.OperatorConfiguration) error {
	// Get scheduler name from configuration
	schedulerName := config.SchedulerName
	if schedulerName == "" {
		schedulerName = "kai-scheduler" // Default to kai-scheduler
	}

	// Initialize global backend with the configured scheduler
	if err := schedulerbackend.Initialize(
		mgr.GetClient(),
		mgr.GetScheme(),
		mgr.GetEventRecorderFor("scheduler-backend"),
		schedulerName,
	); err != nil {
		return fmt.Errorf("failed to initialize scheduler backend: %w", err)
	}
	controllerConfig := config.Controllers
	pcsReconciler := podcliqueset.NewReconciler(mgr, controllerConfig.PodCliqueSet)
	if err := pcsReconciler.RegisterWithManager(mgr); err != nil {
		return err
	}
	pcReconciler := podclique.NewReconciler(mgr, controllerConfig.PodClique)
	if err := pcReconciler.RegisterWithManager(mgr); err != nil {
		return err
	}
	pcsgReconciler := podcliquescalinggroup.NewReconciler(mgr, controllerConfig.PodCliqueScalingGroup)
	if err := pcsgReconciler.RegisterWithManager(mgr); err != nil {
		return err
	}

	// Backend controller for PodGang -> scheduler-specific CR conversion
	backendReconciler, err := backendcontroller.NewReconciler(mgr)
	if err != nil {
		return fmt.Errorf("failed to create backend reconciler: %w", err)
	}
	if err := backendReconciler.RegisterWithManager(mgr); err != nil {
		return err
	}

	return nil
}
