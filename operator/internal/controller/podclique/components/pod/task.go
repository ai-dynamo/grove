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

package pod

import (
	"context"
	"fmt"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// createPodCreationTask creates a utils.Task which will create a Pod, capture the create-expectation and also emit a success/failed event post creation.
func (r _resource) createPodCreationTask(logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pclq *grovecorev1alpha1.PodClique, podGangName, pclqExpectationsKey string, taskIndex, podHostNameIndex int) utils.Task {
	pclqObjKey := client.ObjectKeyFromObject(pclq)
	return utils.Task{
		Name: fmt.Sprintf("CreatePod-%s-%d", pclq.Name, taskIndex),
		Fn: func(ctx context.Context) error {
			pod := &corev1.Pod{}
			// build the Pod resource
			if err := r.buildResource(pcs, pclq, podGangName, pod, podHostNameIndex); err != nil {
				return groveerr.WrapError(err,
					errCodeBuildPodResource,
					component.OperationSync,
					fmt.Sprintf("failed to build Pod resource for PodClique %v", pclqObjKey),
				)
			}
			// create the Pod
			if err := r.client.Create(ctx, pod); err != nil {
				r.eventRecorder.Eventf(pclq, corev1.EventTypeWarning, constants.ReasonPodCreateFailed, "Error creating pod %v: %v", pod.Name, err)
				return groveerr.WrapError(err,
					errCodeCreatePod,
					component.OperationSync,
					fmt.Sprintf("failed to create Pod: %s for PodClique %v", pod.Name, pclqObjKey),
				)
			}
			logger.Info("Created Pod for PodClique", "podName", pod.Name, "podUID", pod.GetUID())
			if err := r.expectationsStore.ExpectCreations(logger, pclqExpectationsKey, pod.GetUID(), podHostNameIndex); err != nil {
				utilruntime.HandleErrorWithLogger(logger, err, "could not record create expectations for Pod", "pclqObjKey", pclqObjKey, "pod", pod.Name)
			}
			r.eventRecorder.Eventf(pclq, corev1.EventTypeNormal, constants.ReasonPodCreateSuccessful, "Created Pod: %s", pod.Name)
			return nil
		},
	}
}

// createPodDeletionTask creates a utils.Task which will delete a Pod, capture the delete-expectation and also emit a success/failed event post deletion.
func (r _resource) createPodDeletionTask(logger logr.Logger, pclq *grovecorev1alpha1.PodClique, podToDelete *corev1.Pod, pclqExpectationsKey string) utils.Task {
	podObjKey := client.ObjectKeyFromObject(podToDelete)
	pclqObjKey := client.ObjectKeyFromObject(pclq)
	return utils.Task{
		Name: fmt.Sprintf("DeletePod-%s", podToDelete.Name),
		Fn: func(ctx context.Context) error {
			if err := r.client.Delete(ctx, podToDelete); err != nil {
				if apierrors.IsNotFound(err) {
					logger.Info("pod has already been deleted", "pod", podObjKey)
					r.expectationsStore.ObserveDeletions(logger, pclqExpectationsKey, podToDelete.GetUID())
					return nil
				}
				r.eventRecorder.Eventf(pclq, corev1.EventTypeWarning, constants.ReasonPodDeleteFailed, "Error deleting pod: %v", err)
				return groveerr.WrapError(err,
					errCodeDeletePod,
					component.OperationSync,
					fmt.Sprintf("failed to delete Pod: %v for PodClique %v", podObjKey, pclqObjKey),
				)
			}

			logger.Info("Deleted Pod", "podObjectKey", podObjKey)
			if err := r.expectationsStore.ExpectDeletions(logger, pclqExpectationsKey, podToDelete.GetUID()); err != nil {
				utilruntime.HandleErrorWithLogger(logger, err, "could not record delete expectation", "pclq", pclqObjKey, "pod", podObjKey)
			}
			r.eventRecorder.Eventf(pclq, corev1.EventTypeNormal, constants.ReasonPodDeleteSuccessful, "Deleted Pod: %s", podToDelete.Name)
			return nil
		},
	}
}

// createPodDeletionTasks creates multiple deletion tasks for a batch of pods
func (r _resource) createPodDeletionTasks(logger logr.Logger, pclq *grovecorev1alpha1.PodClique, podsToDelete []*corev1.Pod, pclqExpectationsKey string) []utils.Task {
	deletionTasks := make([]utils.Task, 0, len(podsToDelete))
	for _, podToDelete := range podsToDelete {
		deletionTasks = append(deletionTasks, r.createPodDeletionTask(logger, pclq, podToDelete, pclqExpectationsKey))
	}
	return deletionTasks
}
