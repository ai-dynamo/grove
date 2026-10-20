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

package scheduler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const workloadControlPlaneTimeout = time.Minute

// StartWorkloadEnvtest starts an isolated API server with explicit WAS gates.
func StartWorkloadEnvtest(t *testing.T, featureGates string, crdPaths ...string) *rest.Config {
	t.Helper()
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:        crdPaths,
		ErrorIfCRDPathMissing:    true,
		ControlPlaneStartTimeout: workloadControlPlaneTimeout,
		ControlPlaneStopTimeout:  workloadControlPlaneTimeout,
	}
	testEnv.ControlPlane.GetAPIServer().Configure().
		Set("feature-gates", featureGates).
		Set("runtime-config", "scheduling.k8s.io/v1beta1=true,scheduling.k8s.io/v1alpha3=true")
	config, err := testEnv.Start()
	t.Cleanup(func() { require.NoError(t, testEnv.Stop()) })
	require.NoError(t, err)
	return config
}
