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
//
// Run as "grove-install-crds prepare-downgrade", it instead prepares the grove.io
// CRDs for a downgrade to a release that serves only grove.io/v1alpha1. The
// operator of this release must still be running, because PodCliqueSets are
// converted by its conversion webhook.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"
	operatorcrds "github.com/ai-dynamo/grove/operator/api/core/crds"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/crdinstaller"
	grovelogger "github.com/ai-dynamo/grove/operator/internal/logger"
	"github.com/ai-dynamo/grove/operator/internal/storageversion"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	schedulercrds "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1/crds"
	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const prepareDowngradeCommand = "prepare-downgrade"

func main() {
	log := grovelogger.MustNewLogger(false, configv1beta1.InfoLevel, configv1beta1.LogFormatJSON)
	ctrl.SetLogger(log)
	logger := ctrl.Log.WithName("grove-install-crds")

	flag.Parse()
	command := flag.Arg(0)
	if command != "" && command != prepareDowngradeCommand {
		logger.Error(fmt.Errorf("unknown command %q", command), "usage: grove-install-crds [prepare-downgrade]")
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

	if command == prepareDowngradeCommand {
		if err = prepareDowngrade(ctx, cl, logger); err != nil {
			logger.Error(err, "failed to prepare the CRDs for the downgrade")
			os.Exit(1)
		}
		logger.Info("The grove.io CRDs serve and store only grove.io/v1alpha1; the release can now be downgraded")
		return
	}

	namespace, err := k8sutils.OperatorNamespace()
	if err != nil {
		logger.Error(err, "failed to read the operator namespace")
		os.Exit(1)
	}

	logger.Info("Installing Grove CRDs", "conversionService", k8sutils.OperatorServiceName(), "conversionServiceNamespace", namespace)
	if err = crdinstaller.InstallCRDs(ctx, cl, logger, allCRDs(), k8sutils.OperatorServiceName(), namespace); err != nil {
		logger.Error(err, "failed to install CRDs")
		os.Exit(1)
	}
	logger.Info("All Grove CRDs installed successfully")
}

// prepareDowngrade makes grove.io/v1alpha1 the only stored and served version of the grove.io CRDs, so that
// the CRDs of a release that knows only v1alpha1 can replace them. It first makes v1alpha1 the storage version
// while both versions are served, then rewrites every object so that it is stored at v1alpha1, and finally
// removes v1beta1 and the conversion webhook. Every step is idempotent, so a failed run can be repeated.
func prepareDowngrade(ctx context.Context, cl client.Client, logger logr.Logger) error {
	alpha := grovecorev1alpha1.SchemeGroupVersion.Version
	beta := grovecorev1beta1.SchemeGroupVersion.Version
	if err := storageversion.SetStorageVersion(ctx, cl, logger, storageversion.CRDNames, alpha); err != nil {
		return err
	}
	if err := storageversion.Migrate(ctx, cl, logger, storageversion.CRDNames, alpha); err != nil {
		return err
	}
	return storageversion.RemoveVersion(ctx, cl, logger, storageversion.CRDNames, beta)
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
