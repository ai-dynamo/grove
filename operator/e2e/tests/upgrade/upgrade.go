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
	// pgmRecoverTimeout bounds the wait for a PodGangMap to drain to empty and then rebuild its anchor
	// as a PodCliqueScalingGroup is scaled below MinAvailable and back. Recovery runs on the next
	// reconcile, so a long wait here signals a real defect.
	pgmRecoverTimeout = 1 * time.Minute

	// groveReleaseName is the Helm release name used for both the install and the upgrade.
	groveReleaseName = "grove"
	// groveNamespace is the namespace the Grove operator runs in.
	groveNamespace = "grove-system"
	// groveReleasedChartRef is the OCI reference of the published Grove chart used for the install.
	groveReleasedChartRef = "oci://ghcr.io/ai-dynamo/grove/grove-charts"
)

// upgradeTest describes an upgrade scenario. The harness installs fromVersion, runs preUpgrade,
// upgrades to the operator built from the current checkout, then runs postUpgrade. preUpgrade owns
// workload setup and any assertions on the fromVersion operator. postUpgrade holds the assertions on
// the upgraded operator. preUpgrade may be nil.
type upgradeTest struct {
	// fromVersion is the released Grove version to install first. The harness never defaults it, so
	// each test states the version it exercises.
	fromVersion string
	// nodeWorkerCount is the number of worker nodes the cluster is prepared with.
	nodeWorkerCount int
	// prepareOpts are passed to PrepareTest, so a test declares its workload with WithWorkload.
	prepareOpts []testctx.TestOption
	// preUpgrade runs against the fromVersion operator, before the upgrade. It is optional.
	preUpgrade func(t *testing.T, tc *testctx.TestContext)
	// postUpgrade runs against the upgraded operator, after the upgrade.
	postUpgrade func(t *testing.T, tc *testctx.TestContext)
}

// runUpgradeTest installs the fromVersion Grove operator, runs preUpgrade, upgrades to the operator
// built from the current checkout, then runs postUpgrade.
func runUpgradeTest(t *testing.T, cfg upgradeTest) {
	t.Helper()

	if testing.Short() {
		t.Skip("upgrade E2E test is disabled by -short")
	}
	cfg.validate(t)

	testctx.Logger.Infof("preparing test cluster")
	tc, cleanup := testctx.PrepareTest(t.Context(), t, cfg.nodeWorkerCount, cfg.prepareOpts...)

	installReleasedGrove(t, tc, cfg.fromVersion)
	// The upgrade tests share one cluster, so uninstall Grove after the test to free the release name
	// for the next one. This defer is registered before cleanup so it runs last, after cleanup has
	// deleted the workload while the operator is still up to process finalizers.
	defer uninstallGrove(t, tc)
	defer cleanup()

	if cfg.preUpgrade != nil {
		cfg.preUpgrade(t, tc)
	}
	upgradeGrove(t, tc)
	cfg.postUpgrade(t, tc)
}

// validate fails the test when the upgradeTest is not fully specified.
func (cfg upgradeTest) validate(t *testing.T) {
	t.Helper()
	if cfg.fromVersion == "" {
		t.Fatalf("upgradeTest.fromVersion is required")
	}
	if cfg.nodeWorkerCount <= 0 {
		t.Fatalf("upgradeTest.nodeWorkerCount must be greater than zero")
	}
	if cfg.postUpgrade == nil {
		t.Fatalf("upgradeTest.postUpgrade is required")
	}
}

// installReleasedGrove installs the given released Grove version from the published OCI chart and waits
// for the operator to become ready.
func installReleasedGrove(t *testing.T, tc *testctx.TestContext, version string) {
	t.Helper()

	testctx.Logger.Infof("installing released Grove operator %s", version)
	_, err := setup.InstallHelmChart(&setup.HelmInstallConfig{
		RestConfig:      tc.Client.RestConfig,
		ReleaseName:     groveReleaseName,
		Namespace:       groveNamespace,
		ChartRef:        groveReleasedChartRef,
		ChartVersion:    version,
		CreateNamespace: true,
		Wait:            true,
		Timeout:         defaultPollTimeout,
		Values:          map[string]any{"config": map[string]any{"server": map[string]any{"healthProbes": map[string]any{"enable": true}}}},
		HelmLoggerFunc:  testctx.Logger.Infof,
	})
	require.NoError(t, err, "install released Grove chart")

	podsManager := pods.NewPodManager(tc.Client, testctx.Logger)
	require.NoError(t, podsManager.WaitForReadyInNamespace(t.Context(), groveNamespace, 1, defaultPollTimeout, defaultPollInterval),
		"waiting for Grove to become ready")
}

// uninstallGrove removes the Grove release so the shared cluster is clean for the next upgrade test.
func uninstallGrove(t *testing.T, tc *testctx.TestContext) {
	t.Helper()
	require.NoError(t, setup.UninstallHelmChart(&setup.HelmInstallConfig{
		RestConfig:     tc.Client.RestConfig,
		ReleaseName:    groveReleaseName,
		Namespace:      groveNamespace,
		Timeout:        defaultPollTimeout,
		HelmLoggerFunc: testctx.Logger.Infof,
	}), "uninstall Grove chart")
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
