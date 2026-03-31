// /*
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
// */

// grove-install-crds is a standalone binary that applies all Grove CRDs via
// server-side apply. It is intended to run as the operator Deployment's init
// container so that CRDs are present before the operator process starts.
package main

import (
	"os"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	operatorcrds "github.com/ai-dynamo/grove/operator/api/core/v1alpha1/crds"
	"github.com/ai-dynamo/grove/operator/internal/crdinstaller"
	grovelogger "github.com/ai-dynamo/grove/operator/internal/logger"

	schedulercrds "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1/crds"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// allCRDs returns all Grove CRD YAML definitions to be applied at startup.
func allCRDs() []string {
	return []string{
		operatorcrds.PodCliqueCRD(),
		operatorcrds.PodCliqueSetCRD(),
		operatorcrds.PodCliqueScalingGroupCRD(),
		operatorcrds.ClusterTopologyCRD(),
		schedulercrds.PodGangCRD(),
	}
}

func main() {
	log := grovelogger.MustNewLogger(false, configv1alpha1.InfoLevel, configv1alpha1.LogFormatJSON)
	ctrl.SetLogger(log)
	logger := ctrl.Log.WithName("grove-install-crds")

	ctx := ctrl.SetupSignalHandler()

	cfg, err := rest.InClusterConfig()
	if err != nil {
		logger.Error(err, "failed to get in-cluster config")
		os.Exit(1)
	}

	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		logger.Error(err, "failed to add apiextensionsv1 to scheme")
		os.Exit(1)
	}

	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		logger.Error(err, "failed to create API client")
		os.Exit(1)
	}

	logger.Info("Installing Grove CRDs")
	if err := crdinstaller.InstallCRDs(ctx, cl, logger, allCRDs()); err != nil {
		logger.Error(err, "failed to install CRDs")
		os.Exit(1)
	}
	logger.Info("All Grove CRDs installed successfully")
}
