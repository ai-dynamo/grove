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
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/podclique"
	"github.com/ai-dynamo/grove/operator/internal/controller/podcliquescalinggroup"
	"github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset"
	"github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend"
	backendcontroller "github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend/controller"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"

	_ "github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend/kai"
	_ "github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend/workload"
)

// RegisterControllers registers all controllers with the manager.
func RegisterControllers(mgr ctrl.Manager, logger logr.Logger, controllerConfig configv1alpha1.ControllerConfiguration) error {
	// Initialize global backend manager first
	// This must be done before any controller that uses backends
	backend.InitializeGlobalManager(mgr.GetClient(), mgr.GetScheme(), mgr.GetEventRecorderFor("backend-manager"))
	logger.Info("Initialized global backend manager")

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

	// Setup backend controllers for PodGang -> scheduler-specific CR conversion
	if err := backendcontroller.SetupBackendControllers(mgr, logger); err != nil {
		return err
	}

	return nil
}
