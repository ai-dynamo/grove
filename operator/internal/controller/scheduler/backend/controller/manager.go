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

package controller

import (
	"fmt"

	"github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend/kai"
	"github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend/workload"
	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
)

// SetupBackendControllers sets up all backend controllers with the manager
// Each backend controller watches PodGang resources and converts them to scheduler-specific CRs
func SetupBackendControllers(mgr ctrl.Manager, logger logr.Logger) error {
	logger.Info("Setting up backend controllers")
	
	// Setup KAI backend
	kaiFactory := &kai.Factory{}
	kaiBackendInstance, err := kaiFactory.CreateBackend(mgr.GetClient(), mgr.GetScheme(), mgr.GetEventRecorderFor("backend-kai"))
	if err != nil {
		return fmt.Errorf("failed to create KAI backend: %w", err)
	}
	
	kaiReconciler := &BackendReconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		Backend: kaiBackendInstance,
		Logger:  logger.WithValues("backend", "kai"),
	}
	
	if err := kaiReconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("failed to setup KAI backend controller: %w", err)
	}
	logger.Info("Registered KAI backend controller")
	
	// Setup Workload backend
	workloadFactory := &workload.Factory{}
	workloadBackendInstance, err := workloadFactory.CreateBackend(mgr.GetClient(), mgr.GetScheme(), mgr.GetEventRecorderFor("backend-workload"))
	if err != nil {
		return fmt.Errorf("failed to create Workload backend: %w", err)
	}
	
	workloadReconciler := &BackendReconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		Backend: workloadBackendInstance,
		Logger:  logger.WithValues("backend", "workload"),
	}
	
	if err := workloadReconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("failed to setup Workload backend controller: %w", err)
	}
	logger.Info("Registered Workload backend controller")
	
	logger.Info("Successfully set up all backend controllers")
	return nil
}

