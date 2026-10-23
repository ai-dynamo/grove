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

package utils

import (
	"context"

	"github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	grovectrl "github.com/ai-dynamo/grove/operator/internal/controller/common"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetPodCliqueSet gets the latest PodCliqueSet object. It will usually hit the informer cache. If the object is not found, it will log a message and return DoNotRequeue.
func GetPodCliqueSet(ctx context.Context, cl client.Client, logger logr.Logger, objectKey client.ObjectKey, pcs *v1alpha1.PodCliqueSet) grovectrl.ReconcileStepResult {
	if err := cl.Get(ctx, objectKey, pcs); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("PodCliqueSet not found", "objectKey", objectKey)
			return grovectrl.DoNotRequeue()
		}
		return grovectrl.ReconcileWithErrors("error getting PodCliqueSet", err)
	}
	return grovectrl.ContinueReconcile()
}

// GetPodClique gets the latest PodClique object. It will usually hit the informer cache. If the object is not found, it will log a message and return DoNotRequeue.
func GetPodClique(ctx context.Context, cl client.Client, logger logr.Logger, objectKey client.ObjectKey, pclq *v1alpha1.PodClique, ignoreNotFound bool) grovectrl.ReconcileStepResult {
	if err := cl.Get(ctx, objectKey, pclq); err != nil {
		if ignoreNotFound && apierrors.IsNotFound(err) {
			logger.V(1).Info("PodClique not found", "objectKey", objectKey)
			return grovectrl.DoNotRequeue()
		}
		return grovectrl.ReconcileWithErrors("error getting PodClique", err)
	}
	return grovectrl.ContinueReconcile()
}

// GetPodCliqueScalingGroup gets the latest PodCliqueScalingGroup object. It will usually hit the informer cache. If the object is not found, it will log a message and return DoNotRequeue.
func GetPodCliqueScalingGroup(ctx context.Context, cl client.Client, logger logr.Logger, objectKey client.ObjectKey, pcsg *v1alpha1.PodCliqueScalingGroup) grovectrl.ReconcileStepResult {
	if err := cl.Get(ctx, objectKey, pcsg); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("PodCliqueScalingGroup not found")
			return grovectrl.DoNotRequeue()
		}
		logger.Error(err, "error getting PodCliqueScalingGroup")
		return grovectrl.ReconcileWithErrors("error getting PodCliqueScalingGroup", err)
	}
	return grovectrl.ContinueReconcile()
}
