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

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/cmd/cli"
	"github.com/ai-dynamo/grove/operator/internal/clustertopology"
	grovectrl "github.com/ai-dynamo/grove/operator/internal/controller"
	"github.com/ai-dynamo/grove/operator/internal/controller/cert"
	grovelogger "github.com/ai-dynamo/grove/operator/internal/logger"
	groveversion "github.com/ai-dynamo/grove/operator/internal/version"

	"github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	logger = ctrl.Log.WithName("grove-setup")
)

func main() {
	ctrl.SetLogger(grovelogger.MustNewLogger(false, configv1alpha1.InfoLevel, configv1alpha1.LogFormatJSON))
	groveInfo := groveversion.New()

	launchOpts, err := cli.ParseLaunchOptions(os.Args[1:])
	if err != nil {
		handleErrorAndExit(err, cli.ExitErrParseCLIArgs)
	}
	if launchOpts.Version {
		_, _ = fmt.Fprintf(io.Writer(os.Stdout), "%s %v\n", apicommonconstants.OperatorName, groveInfo)
		os.Exit(cli.ExitSuccess)
	}
	operatorConfig, err := launchOpts.LoadAndValidateOperatorConfig()
	if err != nil {
		logger.Error(err, "failed to load operator config")
		handleErrorAndExit(err, cli.ExitErrLoadOperatorConfig)
	}

	logger.Info("Starting grove operator", "grove-info", groveInfo.Verbose())
	printFlags()

	// Validate MNNVL prerequisites if the feature is enabled
	if err := validateMNNVLPrerequisites(operatorConfig); err != nil {
		logger.Error(err, "MNNVL prerequisites validation failed")
		handleErrorAndExit(err, cli.ExitErrMNNVLPrerequisites)
	}

	mgr, err := grovectrl.CreateManager(operatorConfig)
	if err != nil {
		logger.Error(err, "failed to create grove controller manager")
		handleErrorAndExit(err, cli.ExitErrInitializeManager)
	}

	ctx := ctrl.SetupSignalHandler()

	// Initialize or clean up ClusterTopology based on operator configuration.
	// This must be done before starting the controllers that may depend on the ClusterTopology resource.
	// NOTE: In this version of the operator the synchronization will additionally ensure that the KAI Topology resource
	// is created based on the ClusterTopology. When we introduce support for pluggable scheduler backends,
	// handling of scheduler specified resources will be delegated to the backend scheduler controller.
	cl, err := client.New(mgr.GetConfig(), client.Options{Scheme: mgr.GetScheme()})
	if err != nil {
		logger.Error(err, "failed to create client for topology synchronization", "cluster-topology", corev1alpha1.DefaultClusterTopologyName)
		handleErrorAndExit(err, cli.ExitErrSynchronizeTopology)
	}
	if err = clustertopology.SynchronizeTopology(ctx, cl, logger, operatorConfig); err != nil {
		logger.Error(err, "failed to synchronize cluster topology")
		handleErrorAndExit(err, cli.ExitErrSynchronizeTopology)
	}

	webhookCertsReadyCh := make(chan struct{})
	if err = cert.ManageWebhookCerts(mgr, operatorConfig.Server.Webhooks.ServerCertDir, operatorConfig.Authorizer.Enabled, webhookCertsReadyCh); err != nil {
		logger.Error(err, "failed to setup cert rotation")
		handleErrorAndExit(err, cli.ExitErrInitializeManager)
	}

	if err = grovectrl.SetupHealthAndReadinessEndpoints(mgr, webhookCertsReadyCh); err != nil {
		logger.Error(err, "failed to set up health and readiness for grove controller manager")
		handleErrorAndExit(err, cli.ExitErrInitializeManager)
	}

	// Certificates need to be generated before the webhooks are started, which can only happen once the manager is started.
	// Block while generating the certificates, and then start the webhooks.
	go func() {
		if err = grovectrl.RegisterControllersAndWebhooks(mgr, logger, operatorConfig, webhookCertsReadyCh); err != nil {
			logger.Error(err, "failed to initialize grove controller manager")
			handleErrorAndExit(err, cli.ExitErrInitializeManager)
		}
	}()

	logger.Info("Starting manager")
	if err = mgr.Start(ctx); err != nil {
		logger.Error(err, "Error starting controller manager")
		handleErrorAndExit(err, cli.ExitErrStart)
	}
}

func printFlags() {
	var flagKVs []any
	flag.VisitAll(func(f *flag.Flag) {
		flagKVs = append(flagKVs, f.Name, f.Value.String())
	})
	logger.Info("Running with flags", flagKVs...)
}

// validateMNNVLPrerequisites checks if MNNVL prerequisites are met when the feature is enabled.
// If MNNVL is enabled, it verifies that the ComputeDomain CRD is installed in the cluster.
func validateMNNVLPrerequisites(operatorCfg *configv1alpha1.OperatorConfiguration) error {
	if !operatorCfg.MNNVL.Enabled {
		return nil
	}

	logger.Info("MNNVL support is enabled, validating prerequisites")

	// Check for ComputeDomain CRD using discovery client
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to get cluster config for MNNVL validation: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("failed to create discovery client for MNNVL validation: %w", err)
	}

	// Check if the ComputeDomain CRD exists
	_, apiResourceList, err := discoveryClient.ServerGroupsAndResources()
	if err != nil {
		// Handle partial discovery errors gracefully
		if !discovery.IsGroupDiscoveryFailedError(err) {
			return fmt.Errorf("failed to discover API resources: %w", err)
		}
	}

	if !isComputeDomainCRDPresent(apiResourceList) {
		return fmt.Errorf("MNNVL is enabled but ComputeDomain CRD (%s) is not installed. "+
			"Please install the NVIDIA DRA driver or disable MNNVL in the operator configuration", apicommonconstants.ComputeDomainCRDName)
	}

	logger.Info("ComputeDomain CRD found, MNNVL prerequisites validated")
	return nil
}

// isComputeDomainCRDPresent checks if the ComputeDomain CRD is present in the API resource list.
// This is extracted as a separate function to enable unit testing.
func isComputeDomainCRDPresent(apiResourceList []*metav1.APIResourceList) bool {
	computeDomainGroupVersion := apicommonconstants.ComputeDomainGroup + "/" + apicommonconstants.ComputeDomainVersion
	for _, resourceList := range apiResourceList {
		for _, resource := range resourceList.APIResources {
			if resource.Name == apicommonconstants.ComputeDomainResource && resourceList.GroupVersion == computeDomainGroupVersion {
				return true
			}
		}
	}
	return false
}

// handleErrorAndExit gracefully handles errors before exiting the program.
func handleErrorAndExit(err error, exitCode int) {
	if errors.Is(err, pflag.ErrHelp) {
		os.Exit(cli.ExitSuccess)
	}
	_, _ = fmt.Fprintf(os.Stderr, "Err: %v\n", err)
	os.Exit(exitCode)
}
