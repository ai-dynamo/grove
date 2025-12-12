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
	"context"
	"flag"
	"fmt"
	"os"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveopts "github.com/ai-dynamo/grove/operator/cmd/opts"
	grovectrl "github.com/ai-dynamo/grove/operator/internal/controller"
	"github.com/ai-dynamo/grove/operator/internal/controller/cert"
	grovelogger "github.com/ai-dynamo/grove/operator/internal/logger"
	groveversion "github.com/ai-dynamo/grove/operator/internal/version"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	logger = ctrl.Log.WithName("grove-setup")
)

func main() {
	ctrl.SetLogger(grovelogger.MustNewLogger(false, configv1alpha1.InfoLevel, configv1alpha1.LogFormatJSON))

	fs := pflag.CommandLine
	groveversion.AddFlags(fs)
	cliOpts := groveopts.NewCLIOptions(fs)

	// parse and print command line flags
	pflag.Parse()
	groveversion.PrintVersionAndExitIfRequested()

	logger.Info("Starting grove operator", "version", groveversion.Get())
	printFlags()

	operatorCfg, err := initializeOperatorConfig(cliOpts)
	if err != nil {
		logger.Error(err, "failed to initialize operator configuration")
		os.Exit(1)
	}

	mgr, err := grovectrl.CreateManager(operatorCfg)
	if err != nil {
		logger.Error(err, "failed to create grove controller manager")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()

	if err = validateClusterTopology(ctx, mgr.GetAPIReader(), operatorCfg.ClusterTopology); err != nil {
		logger.Error(err, "cannot validate cluster topology, operator cannot start")
		os.Exit(1)
	}

	webhookCertsReadyCh := make(chan struct{})
	if err = cert.ManageWebhookCerts(
		mgr,
		operatorCfg.Server.Webhooks.ServerCertDir,
		operatorCfg.Server.Webhooks.SecretName,
		operatorCfg.Authorizer.Enabled,
		*operatorCfg.Server.Webhooks.AutoProvision,
		webhookCertsReadyCh,
	); err != nil {
		logger.Error(err, "failed to setup certificate management")
		os.Exit(1)
	}

	if err = grovectrl.SetupHealthAndReadinessEndpoints(mgr, webhookCertsReadyCh); err != nil {
		logger.Error(err, "failed to set up health and readiness for grove controller manager")
		os.Exit(1)
	}

	// Certificates need to be generated before the webhooks are started, which can only happen once the manager is started.
	// Block while generating the certificates, and then start the webhooks.
	go func() {
		if err = grovectrl.RegisterControllersAndWebhooks(mgr, logger, operatorCfg, webhookCertsReadyCh); err != nil {
			logger.Error(err, "failed to initialize grove controller manager")
			os.Exit(1)
		}
	}()

	logger.Info("Starting manager")
	if err = mgr.Start(ctx); err != nil {
		logger.Error(err, "Error running manager")
		os.Exit(1)
	}
}

func initializeOperatorConfig(cliOpts *groveopts.CLIOptions) (*configv1alpha1.OperatorConfiguration, error) {
	// complete and validate operator configuration
	if err := cliOpts.Complete(); err != nil {
		return nil, err
	}
	if err := cliOpts.Validate(); err != nil {
		return nil, err
	}
	return cliOpts.Config, nil
}

func printFlags() {
	var flagKVs []any
	flag.VisitAll(func(f *flag.Flag) {
		flagKVs = append(flagKVs, f.Name, f.Value.String())
	})
	logger.Info("Running with flags", flagKVs...)
}

func validateClusterTopology(ctx context.Context, reader client.Reader, clusterTopologyConfig configv1alpha1.ClusterTopologyConfiguration) error {
	if !clusterTopologyConfig.Enabled {
		return nil
	}
	var clusterTopology grovecorev1alpha1.ClusterTopology
	if err := reader.Get(ctx, types.NamespacedName{Name: clusterTopologyConfig.Name}, &clusterTopology); err != nil {
		return fmt.Errorf("failed to fetch ClusterTopology %s: %w", clusterTopologyConfig.Name, err)
	}
	logger.Info("topology validated successfully", "cluster topology", clusterTopologyConfig.Name)
	return nil
}
