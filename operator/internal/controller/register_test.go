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
	"testing"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestRegisterControllers tests registration of all Grove controllers with the manager.
// This test verifies that each controller (PodCliqueSet, PodClique, PodCliqueScalingGroup)
// can be successfully created and registered without errors when provided with a valid
// controller configuration.
func TestRegisterControllers(t *testing.T) {
	// Check if kubebuilder binaries are available for testing
	testEnv := &envtest.Environment{}
	cfg, err := testEnv.Start()
	if err != nil {
		t.Skipf("Skipping test: kubebuilder test environment not available: %v", err)
		return
	}
	defer func() {
		err := testEnv.Stop()
		require.NoError(t, err)
	}()

	// Test successful registration with valid configuration
	t.Run("successful registration", func(t *testing.T) {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{})
		require.NoError(t, err)

		controllerConfig := configv1alpha1.ControllerConfiguration{
			PodCliqueSet: configv1alpha1.PodCliqueSetControllerConfiguration{
				ConcurrentSyncs: ptr.To(1),
			},
			PodClique: configv1alpha1.PodCliqueControllerConfiguration{
				ConcurrentSyncs: ptr.To(1),
			},
			PodCliqueScalingGroup: configv1alpha1.PodCliqueScalingGroupControllerConfiguration{
				ConcurrentSyncs: ptr.To(1),
			},
		}

		err = RegisterControllers(mgr, controllerConfig, configv1alpha1.TopologyAwareSchedulingConfiguration{}, configv1alpha1.NetworkAcceleration{})
		require.NoError(t, err)
	})

	// Test registration with different concurrency settings
	t.Run("registration with higher concurrency", func(t *testing.T) {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{})
		require.NoError(t, err)

		controllerConfig := configv1alpha1.ControllerConfiguration{
			PodCliqueSet: configv1alpha1.PodCliqueSetControllerConfiguration{
				ConcurrentSyncs: ptr.To(5),
			},
			PodClique: configv1alpha1.PodCliqueControllerConfiguration{
				ConcurrentSyncs: ptr.To(10),
			},
			PodCliqueScalingGroup: configv1alpha1.PodCliqueScalingGroupControllerConfiguration{
				ConcurrentSyncs: ptr.To(3),
			},
		}

		err = RegisterControllers(mgr, controllerConfig, configv1alpha1.TopologyAwareSchedulingConfiguration{}, configv1alpha1.NetworkAcceleration{})
		require.NoError(t, err)
	})
}
