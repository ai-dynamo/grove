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

	"github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
)

// SetupBackendControllers sets up all backend controllers with the manager
// Each backend controller watches PodGang resources and converts them to scheduler-specific CRs
func SetupBackendControllers(mgr ctrl.Manager, logger logr.Logger) error {
	logger.Info("Setting up backend controllers")

	// Get the global backend manager
	backendManager, err := backend.GetGlobalManager()
	if err != nil {
		return fmt.Errorf("backend manager not initialized: %w", err)
	}

	// Get all backends from the manager
	backends := backendManager.GetAllBackends()

	// Setup a controller for each backend
	for _, be := range backends {
		reconciler := &BackendReconciler{
			Client:  mgr.GetClient(),
			Scheme:  mgr.GetScheme(),
			Backend: be,
			Logger:  logger.WithValues("backend", be.Name()),
		}

		if err := reconciler.SetupWithManager(mgr); err != nil {
			return fmt.Errorf("failed to setup %s backend controller: %w", be.Name(), err)
		}
		logger.Info("Registered backend controller", "backend", be.Name())
	}

	logger.Info("Successfully set up all backend controllers")
	return nil
}
