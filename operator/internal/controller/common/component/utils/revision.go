// /*
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
// */

package utils

import (
	"context"
	"fmt"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	commonrevision "github.com/ai-dynamo/grove/operator/internal/controller/common/revision"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetSelectedPodCliqueSetRevision loads and validates the revision selected by the PodCliqueSet.
func GetSelectedPodCliqueSetRevision(ctx context.Context, cl client.Client, pcs *grovecorev1alpha1.PodCliqueSet) (*commonrevision.SelectedRevision, error) {
	if pcs.Status.CurrentRevision == nil || *pcs.Status.CurrentRevision == "" {
		return nil, fmt.Errorf("PodCliqueSet %v has no selected ControllerRevision", client.ObjectKeyFromObject(pcs))
	}
	controllerRevision := &appsv1.ControllerRevision{}
	if err := cl.Get(ctx, client.ObjectKey{Namespace: pcs.Namespace, Name: *pcs.Status.CurrentRevision}, controllerRevision); err != nil {
		return nil, err
	}
	if !metav1.IsControlledBy(controllerRevision, pcs) {
		return nil, fmt.Errorf("ControllerRevision %v is not controlled by PodCliqueSet %v", client.ObjectKeyFromObject(controllerRevision), client.ObjectKeyFromObject(pcs))
	}
	selectedRevision, err := commonrevision.DecodeSelectedRevision(controllerRevision.Data.Raw)
	if err != nil {
		return nil, fmt.Errorf("ControllerRevision %v has invalid revision data: %w", client.ObjectKeyFromObject(controllerRevision), err)
	}
	if pcs.Status.CurrentGenerationHash == nil || *pcs.Status.CurrentGenerationHash != selectedRevision.GenerationHash() {
		return nil, fmt.Errorf("PodCliqueSet %v generation identity does not match ControllerRevision %v", client.ObjectKeyFromObject(pcs), client.ObjectKeyFromObject(controllerRevision))
	}
	return selectedRevision, nil
}
