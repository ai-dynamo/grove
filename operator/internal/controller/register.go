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
	"github.com/ai-dynamo/grove/operator/internal/controller/podgang"

	ctrl "sigs.k8s.io/controller-runtime"
)

// RegisterControllers registers all controllers with the manager.
func RegisterControllers(mgr ctrl.Manager, config configv1alpha1.OperatorConfiguration) error {
	pcsReconciler := podcliqueset.NewReconciler(mgr, config.Controllers.PodCliqueSet, config.TopologyAwareScheduling, config.Network)
	if err := pcsReconciler.RegisterWithManager(mgr); err != nil {
		return err
	}
	pcReconciler := podclique.NewReconciler(mgr, config.Controllers.PodClique)
	if err := pcReconciler.RegisterWithManager(mgr); err != nil {
		return err
	}
	pcsgReconciler := podcliquescalinggroup.NewReconciler(mgr, config.Controllers.PodCliqueScalingGroup)
	if err := pcsgReconciler.RegisterWithManager(mgr); err != nil {
		return err
	}

	// PodGang controller: PodGang -> scheduler-specific CR conversion
	podgangReconciler, err := podgang.NewReconciler(mgr)
	if err != nil {
		return fmt.Errorf("failed to create podgang reconciler: %w", err)
	}
	if err = podgangReconciler.RegisterWithManager(mgr); err != nil {
		return err
	}

	return nil
}
