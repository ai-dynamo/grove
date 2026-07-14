//go:build e2e && e2eupgrade

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

package upgrade

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/stretchr/testify/require"
)

const (
	// defaultPollTimeout is the default timeout for polling operations
	defaultPollTimeout = 5 * time.Minute
	// defaultPollInterval is the interval for most polling conditions
	defaultPollInterval = 5 * time.Second
)

// testConfig holds configuration for upgrade test setup.
type testConfig struct {
	// Required - initial version installed
	fromVersion string

	// Required - workload that will be installed before and verified after an upgrade.
	workload *testctx.WorkloadConfig
}

// setupTest initializes an upgrade test with the given configuration.
// It handles:
// 1. Cluster preparation
// 2. TestContext creation with standard parameters
// 3. Workload deployment and pod verification
// 4. Installation of the given version
//
// Returns:
//   - tc: TestContext for the test
func setupTest(t *testing.T, cfg testConfig) *testctx.TestContext {
	t.Helper()

	if testing.Short() {
		t.Skip("upgrade E2E test is disabled by -short")
	}

	if cfg.fromVersion == "" {
		t.Fatalf("TestConfig.FromVersion is required")
	}

	if cfg.workload == nil {
		t.Fatalf("TestConfig.Workload is required")
	}

	testctx.Logger.Infof("preparing test cluster")

	tc, cleanup := testctx.PrepareTest(
		t.Context(),
		t,
		1,
		testctx.WithWorkload(cfg.workload),
	)
	t.Cleanup(cleanup)

	testctx.Logger.Infof("testing Grove upgrade from %s to the current checkout", cfg.fromVersion)
	testctx.Logger.Info("installing released Grove operator")

	_, err := setup.InstallHelmChart(&setup.HelmInstallConfig{
		RestConfig:      tc.Client.RestConfig,
		ReleaseName:     "grove",
		Namespace:       "grove-system",
		ChartRef:        "oci://ghcr.io/ai-dynamo/grove/grove-charts",
		ChartVersion:    cfg.fromVersion,
		CreateNamespace: true,
		Wait:            true,
		Timeout:         defaultPollTimeout,
		Values:          map[string]any{"config": map[string]any{"server": map[string]any{"healthProbes": map[string]any{"enable": true}}}},
		HelmLoggerFunc:  testctx.Logger.Infof,
	})
	require.NoError(t, err, "install released Grove chart")

	podsManager := pods.NewPodManager(tc.Client, testctx.Logger)
	err = podsManager.WaitForReadyInNamespace(t.Context(), "grove-system", 1, defaultPollTimeout, defaultPollInterval)
	require.NoError(t, err, "waiting for Grove to become ready")

	testctx.Logger.Info("applying workload before upgrade")

	_, err = tc.DeployAndVerifyWorkload()
	require.NoError(t, err, "applying workload")

	err = podsManager.WaitForReadyInNamespace(t.Context(), cfg.workload.Namespace, 2, defaultPollTimeout, defaultPollInterval)
	require.NoError(t, err, "waiting for workload to become ready")

	return tc
}

// upgradeGrove handles the upgrade of Grove to the current codebase.
func upgradeGrove(t *testing.T, tc *testctx.TestContext) {
	t.Helper()

	testctx.Logger.Info("building current Grove operator")

	rootDir, err := setup.GetOperatorRootDir()
	require.NoError(t, err, "locate current root directory")

	_, err = setup.BuildWithSkaffold(t.Context(), &setup.SkaffoldInstallConfig{
		SkaffoldYAMLPath: filepath.Join(rootDir, "skaffold.yaml"),
		RestConfig:       tc.Client.RestConfig,
		PushRepo:         "localhost:5001",
		PullRepo:         "registry:5001",
		Env: map[string]string{
			"VERSION": "latest",
			"LD_FLAGS": "-X github.com/ai-dynamo/grove/operator/internal/version.gitCommit=e2e-upgrade-commit " +
				"-X github.com/ai-dynamo/grove/operator/internal/version.gitTreeState=clean " +
				"-X github.com/ai-dynamo/grove/operator/internal/version.buildDate=now " +
				"-X github.com/ai-dynamo/grove/operator/internal/version.gitVersion=latest",
		},
		Logger: testctx.Logger,
	})
	require.NoError(t, err, "building new operator images")

	testctx.Logger.Info("upgrading to current Grove operator")

	chartDir, err := setup.GetGroveChartDir()
	require.NoError(t, err, "locate current Grove chart")
	chartVersion, err := setup.GetGroveChartVersion(chartDir)
	require.NoError(t, err, "read current Grove chart version")
	_, err = setup.UpgradeHelmChart(&setup.HelmInstallConfig{
		RestConfig:   tc.Client.RestConfig,
		ReleaseName:  "grove",
		Namespace:    "grove-system",
		ChartRef:     chartDir,
		ChartVersion: chartVersion,
		Wait:         true,
		Timeout:      defaultPollTimeout,
		Values: map[string]any{
			"config": map[string]any{"server": map[string]any{"healthProbes": map[string]any{"enable": true}}},
			"image": map[string]any{
				"repository": "registry:5001/grove-operator",
				"tag":        "latest",
			},
			"deployment": map[string]any{
				"env": []any{
					map[string]any{
						"name":  "GROVE_INIT_CONTAINER_IMAGE",
						"value": "registry:5001/grove-initc",
					},
				},
			},
			"crdInstaller": map[string]any{
				"enabled": true,
				"image": map[string]any{
					"repository": "registry:5001/grove-install-crds",
					"tag":        "latest",
				},
			},
		},
		HelmLoggerFunc: testctx.Logger.Infof,
	})
	require.NoError(t, err, "upgrade Grove chart to current checkout")
}
