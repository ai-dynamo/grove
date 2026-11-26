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

// grove-install-crds is a standalone binary that applies all Grove CRDs via
// server-side apply. It is intended to run as the operator Deployment's init
// container so that CRDs are present before the operator process starts.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"
	operatorcrds "github.com/ai-dynamo/grove/operator/api/core/crds"
	"github.com/ai-dynamo/grove/operator/cmd/cli"
	"github.com/ai-dynamo/grove/operator/internal/crdinstaller"
	grovelogger "github.com/ai-dynamo/grove/operator/internal/logger"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"
	groveversion "github.com/ai-dynamo/grove/operator/internal/version"

	schedulercrds "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1/crds"
	"github.com/spf13/pflag"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const crdInstallerName = "grove-install-crds"

var logger = ctrl.Log.WithName(crdInstallerName)

func main() {
	ctrl.SetLogger(grovelogger.MustNewLogger(false, configv1beta1.InfoLevel, configv1beta1.LogFormatJSON))
	groveInfo := groveversion.New()

	launchOpts, err := cli.ParseCRDInstallerOptions(os.Args[1:])
	if err != nil {
		handleErrorAndExit(err, cli.ExitErrParseCLIArgs)
	}
	if launchOpts.Version {
		_, _ = fmt.Fprintf(io.Writer(os.Stdout), "%s %v\n", crdInstallerName, groveInfo)
		os.Exit(cli.ExitSuccess)
	}

	logger.Info("Starting grove CRD installer", "grove-info", groveInfo.Verbose())

	ctx := ctrl.SetupSignalHandler()

	// The in-cluster config is used when running as a Pod, and a kubeconfig otherwise.
	cfg, err := ctrl.GetConfig()
	if err != nil {
		logger.Error(err, "failed to get the API server config")
		handleErrorAndExit(err, cli.ExitErrInitializeCRDInstaller)
	}

	scheme := runtime.NewScheme()
	if err = apiextensionsv1.AddToScheme(scheme); err != nil {
		logger.Error(err, "failed to add apiextensionsv1 to scheme")
		handleErrorAndExit(err, cli.ExitErrInitializeCRDInstaller)
	}

	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		logger.Error(err, "failed to create API client")
		handleErrorAndExit(err, cli.ExitErrInitializeCRDInstaller)
	}

	namespace, err := k8sutils.OperatorNamespace()
	if err != nil {
		logger.Error(err, "failed to read the operator namespace")
		handleErrorAndExit(err, cli.ExitErrLoadCRDInstallerConfig)
	}
	// In manual provisioning mode, the init container injects this CA bundle into the conversion webhook config.
	var caBundle []byte
	if launchOpts.ConversionWebhookCAFile != "" {
		caBundle, err = os.ReadFile(filepath.Clean(launchOpts.ConversionWebhookCAFile))
		if err != nil {
			logger.Error(err, "failed to read the conversion webhook CA file", "path", launchOpts.ConversionWebhookCAFile)
			handleErrorAndExit(err, cli.ExitErrLoadCRDInstallerConfig)
		}
		if len(caBundle) == 0 {
			err = errors.New("CA file is empty")
			logger.Error(err, "failed to read the conversion webhook CA file", "path", launchOpts.ConversionWebhookCAFile)
			handleErrorAndExit(err, cli.ExitErrLoadCRDInstallerConfig)
		}
	}

	conversion := crdinstaller.ConversionWebhookConfig{
		ServiceName:      k8sutils.OperatorServiceName(),
		ServiceNamespace: namespace,
		ServicePort:      launchOpts.ConversionWebhookPort,
		CABundle:         caBundle,
	}
	logger.Info("Configuring Grove CRDs", "upgradeCRDs", launchOpts.UpgradeCRDs,
		"conversionService", conversion.ServiceName, "conversionServiceNamespace", conversion.ServiceNamespace,
		"conversionServicePort", conversion.ServicePort)
	if launchOpts.UpgradeCRDs {
		if err = crdinstaller.InstallCRDs(ctx, cl, logger, allCRDs(), conversion); err != nil {
			logger.Error(err, "failed to upgrade CRDs")
			handleErrorAndExit(err, cli.ExitErrUpgradeCRDs)
		}
		logger.Info("All Grove CRDs upgraded successfully")
		return
	}

	if err = crdinstaller.ConfigureConversionWebhook(ctx, cl, logger, operatorcrds.PodCliqueSetCRD(), conversion); err != nil {
		logger.Error(err, "failed to configure the PodCliqueSet conversion webhook")
		handleErrorAndExit(err, cli.ExitErrConfigureCRDConversion)
	}
}

// allCRDs returns all Grove CRD YAML definitions to be applied at startup.
func allCRDs() []string {
	return []string{
		operatorcrds.PodCliqueCRD(),
		operatorcrds.PodCliqueSetCRD(),
		operatorcrds.PodCliqueScalingGroupCRD(),
		operatorcrds.ClusterTopologyCRD(),
		operatorcrds.PodGangMapCRD(),
		schedulercrds.PodGangCRD(),
	}
}

// handleErrorAndExit gracefully handles errors before exiting the program.
func handleErrorAndExit(err error, exitCode int) {
	if errors.Is(err, pflag.ErrHelp) {
		os.Exit(cli.ExitSuccess)
	}
	_, _ = fmt.Fprintf(os.Stderr, "Err: %v\n", err)
	os.Exit(exitCode)
}
