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
	"flag"
	"fmt"
	"os"
	"path/filepath"

	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"
	operatorcrds "github.com/ai-dynamo/grove/operator/api/core/crds"
	"github.com/ai-dynamo/grove/operator/internal/crdinstaller"
	grovelogger "github.com/ai-dynamo/grove/operator/internal/logger"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	schedulercrds "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1/crds"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const defaultConversionWebhookPort = 9443

func main() {
	log := grovelogger.MustNewLogger(false, configv1beta1.InfoLevel, configv1beta1.LogFormatJSON)
	ctrl.SetLogger(log)
	logger := ctrl.Log.WithName("grove-install-crds")

	upgradeCRDs := flag.Bool("upgrade-crds", false, "upgrade CRDs from the bundle shipped with this release")
	conversionWebhookPort := flag.Int("conversion-webhook-port", defaultConversionWebhookPort, "port of the conversion webhook Service")
	// This is only relevant when manual certificate management is done by the user
	conversionWebhookCAFile := flag.String("conversion-webhook-ca-file", "", "file containing the conversion webhook CA certificate")
	flag.Parse()
	if flag.NArg() != 0 {
		logger.Error(fmt.Errorf("unexpected arguments %q", flag.Args()), "usage: grove-install-crds")
		os.Exit(2)
	}

	ctx := ctrl.SetupSignalHandler()

	// The in-cluster config is used when running as a Pod, and a kubeconfig otherwise.
	cfg, err := ctrl.GetConfig()
	if err != nil {
		logger.Error(err, "failed to get the API server config")
		os.Exit(1)
	}

	scheme := runtime.NewScheme()
	if err = apiextensionsv1.AddToScheme(scheme); err != nil {
		logger.Error(err, "failed to add apiextensionsv1 to scheme")
		os.Exit(1)
	}

	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		logger.Error(err, "failed to create API client")
		os.Exit(1)
	}

	namespace, err := k8sutils.OperatorNamespace()
	if err != nil {
		logger.Error(err, "failed to read the operator namespace")
		os.Exit(1)
	}
	// In manual provisioning mode, the init container injects this CA bundle into the conversion webhook config.
	var caBundle []byte
	if *conversionWebhookCAFile != "" {
		caBundle, err = os.ReadFile(filepath.Clean(*conversionWebhookCAFile))
		if err != nil {
			logger.Error(err, "failed to read the conversion webhook CA file", "path", *conversionWebhookCAFile)
			os.Exit(1)
		}
		if len(caBundle) == 0 {
			logger.Error(fmt.Errorf("CA file is empty"), "failed to read the conversion webhook CA file", "path", *conversionWebhookCAFile)
			os.Exit(1)
		}
	}

	conversion := crdinstaller.ConversionWebhookConfig{
		ServiceName:      k8sutils.OperatorServiceName(),
		ServiceNamespace: namespace,
		ServicePort:      *conversionWebhookPort,
		CABundle:         caBundle,
	}
	logger.Info("Configuring Grove CRDs", "upgradeCRDs", *upgradeCRDs,
		"conversionService", conversion.ServiceName, "conversionServiceNamespace", conversion.ServiceNamespace,
		"conversionServicePort", conversion.ServicePort)
	if *upgradeCRDs {
		if err = crdinstaller.InstallCRDs(ctx, cl, logger, allCRDs(), conversion); err != nil {
			logger.Error(err, "failed to upgrade CRDs")
			os.Exit(1)
		}
		logger.Info("All Grove CRDs upgraded successfully")
		return
	}

	if err = crdinstaller.ConfigureConversionWebhook(ctx, cl, logger, operatorcrds.PodCliqueSetCRD(), conversion); err != nil {
		logger.Error(err, "failed to configure the PodCliqueSet conversion webhook")
		os.Exit(1)
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
