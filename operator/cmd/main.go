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

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/cmd/cli"
	"github.com/ai-dynamo/grove/operator/internal/clustertopology"
	grovectrl "github.com/ai-dynamo/grove/operator/internal/controller"
	"github.com/ai-dynamo/grove/operator/internal/controller/cert"
	grovelogger "github.com/ai-dynamo/grove/operator/internal/logger"
	"github.com/ai-dynamo/grove/operator/internal/mnnvl"
	"github.com/ai-dynamo/grove/operator/internal/podgangmigrator"
	schedulerregistry "github.com/ai-dynamo/grove/operator/internal/scheduler/registry"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"
	groveversion "github.com/ai-dynamo/grove/operator/internal/version"
	conversionwebhook "github.com/ai-dynamo/grove/operator/internal/webhook/conversion"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/util/wait"
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

	// Run MNNVL preflight checks if the feature is enabled
	if err := mnnvl.Preflight(operatorConfig); err != nil {
		logger.Error(err, "MNNVL preflight check failed")
		handleErrorAndExit(err, cli.ExitErrMNNVLPrerequisites)
	}

	mgr, err := grovectrl.CreateManager(operatorConfig)
	if err != nil {
		logger.Error(err, "failed to create grove controller manager")
		handleErrorAndExit(err, cli.ExitErrInitializeManager)
	}

	ctx := ctrl.SetupSignalHandler()

	// Create a direct (non-cached) client. It is used for pre-manager setup tasks (topology
	// synchronization and webhook certificate provisioning run before mgr.Start(), when the
	// manager's informer cache is not yet available) and is also handed to scheduler backends
	// via Init for runtime reads that cannot tolerate a stale cache.
	cl, err := client.New(mgr.GetConfig(), client.Options{Scheme: mgr.GetScheme()})
	if err != nil {
		logger.Error(err, "failed to create direct API client for pre-start setup")
		handleErrorAndExit(err, cli.ExitErrInitializeManager)
	}

	// Initialize scheduler backends with the configured schedulers.
	schedRegistry, err := schedulerregistry.New(
		mgr.GetClient(),
		cl,
		mgr.GetScheme(),
		mgr.GetEventRecorderFor("scheduler-backend"),
		operatorConfig.Scheduler,
	)
	if err != nil {
		logger.Error(err, "failed to initialize scheduler backend")
		handleErrorAndExit(err, cli.ExitErrInitializeSchedulerBackend)
	}

	// Synchronize backend topologies for all existing ClusterTopologyBinding resources.
	// This must be done before starting the controllers that may depend on the ClusterTopologyBinding resource.
	if err = clustertopology.SynchronizeTopology(ctx, cl, logger, schedRegistry.AllTopologyAware()); err != nil {
		logger.Error(err, "failed to synchronize cluster topology")
		handleErrorAndExit(err, cli.ExitErrSynchronizeTopology)
	}

	// Point the CRDs' conversion webhook at this operator before any controller or client reads a converted kind.
	operatorNamespace, err := k8sutils.OperatorNamespace()
	if err != nil {
		logger.Error(err, "failed to read the operator namespace")
		handleErrorAndExit(err, cli.ExitErrConfigureCRDConversion)
	}
	if err = conversionwebhook.ConfigureCRDs(ctx, cl, logger, operatorNamespace, operatorConfig.Server.Webhooks.Port); err != nil {
		logger.Error(err, "failed to configure the conversion webhook of the CRDs")
		handleErrorAndExit(err, cli.ExitErrConfigureCRDConversion)
	}
	// cert-controller injects the CA in auto mode. In manual mode, the CA of the provided certificates is used.
	caFile := ""
	if operatorConfig.Server.Webhooks.CertProvisionMode == configv1alpha1.CertProvisionModeManual {
		caFile = filepath.Join(operatorConfig.Server.Webhooks.ServerCertDir, "ca.crt")
	}
	if err = mgr.Add(conversionwebhook.NewCRDConversionKeeper(cl, logger, operatorNamespace, operatorConfig.Server.Webhooks.Port, caFile)); err != nil {
		logger.Error(err, "failed to add the CRD conversion keeper")
		handleErrorAndExit(err, cli.ExitErrConfigureCRDConversion)
	}

	webhookCertsReadyCh := make(chan struct{})
	if err = cert.ManageWebhookCerts(
		ctx,
		mgr,
		cl,
		operatorConfig.Server.Webhooks.ServerCertDir,
		operatorConfig.Server.Webhooks.SecretName,
		operatorConfig.Authorizer.Enabled,
		operatorConfig.Server.Webhooks.CertProvisionMode,
		webhookCertsReadyCh,
	); err != nil {
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
		if err := grovectrl.RegisterControllersAndWebhooks(ctx, mgr, logger, operatorConfig, webhookCertsReadyCh, schedRegistry, setPodGangMigrationGate(cl)); err != nil {
			logger.Error(err, "failed to initialize grove controller manager")
			if errors.Is(err, errSetPodGangMigrationGate) {
				handleErrorAndExit(err, cli.ExitErrSetPodGangMigrationGate)
			}
			handleErrorAndExit(err, cli.ExitErrInitializeManager)
		}
	}()

	logger.Info("Starting manager")
	if err = mgr.Start(ctx); err != nil {
		logger.Error(err, "Error starting controller manager")
		handleErrorAndExit(err, cli.ExitErrStart)
	}
}

var errSetPodGangMigrationGate = errors.New("failed to set the PodGang migration gate on legacy PodCliqueSets")

// setPodGangMigrationGate closes the PodGang migration gate on legacy PodCliqueSets before the controllers
// start, so that scaling reconcilers do not race the in-reconcile migration to the epoch-based PodGang scheme.
// PodCliqueSets are read through the conversion webhook, which becomes reachable only once this replica is
// ready and the CRDs carry its CA, so the gate is retried.
func setPodGangMigrationGate(cl client.Client) func(context.Context) error {
	return func(ctx context.Context) error {
		var lastErr error
		if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
			lastErr = podgangmigrator.SetMigrationGateForLegacyPodCliqueSets(ctx, cl, logger)
			if lastErr != nil {
				logger.Info("Retrying the PodGang migration gate", "error", lastErr.Error())
			}
			return lastErr == nil, nil
		}); err != nil {
			return fmt.Errorf("%w: %w", errSetPodGangMigrationGate, errors.Join(err, lastErr))
		}
		return nil
	}
}

func printFlags() {
	var flagKVs []any
	flag.VisitAll(func(f *flag.Flag) {
		flagKVs = append(flagKVs, f.Name, f.Value.String())
	})
	logger.Info("Running with flags", flagKVs...)
}

// handleErrorAndExit gracefully handles errors before exiting the program.
func handleErrorAndExit(err error, exitCode int) {
	if errors.Is(err, pflag.ErrHelp) {
		os.Exit(cli.ExitSuccess)
	}
	_, _ = fmt.Fprintf(os.Stderr, "Err: %v\n", err)
	os.Exit(exitCode)
}
