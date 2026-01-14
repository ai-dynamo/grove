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
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/expect"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// constants for error codes
const (
	errCodeGetPod                              grovecorev1alpha1.ErrorCode = "ERR_GET_POD"
	errCodeDeletePod                           grovecorev1alpha1.ErrorCode = "ERR_DELETE_POD"
	errCodeGetAvailablePodHostNameIndices      grovecorev1alpha1.ErrorCode = "ERR_GET_AVAILABLE_POD_HOSTNAME_INDICES"
	errCodeGetPodGang                          grovecorev1alpha1.ErrorCode = "ERR_GET_PODGANG"
	errCodeGetPodCliqueSet                     grovecorev1alpha1.ErrorCode = "ERR_GET_PODCLIQUESET"
	errCodeGetPodClique                        grovecorev1alpha1.ErrorCode = "ERR_GET_PODCLIQUE"
	errCodeListPod                             grovecorev1alpha1.ErrorCode = "ERR_LIST_POD"
	errCodeRemovePodSchedulingGate             grovecorev1alpha1.ErrorCode = "ERR_REMOVE_POD_SCHEDULING_GATE"
	errCodeCreatePod                           grovecorev1alpha1.ErrorCode = "ERR_CREATE_POD"
	errCodeMissingPodGangLabelOnPCLQ           grovecorev1alpha1.ErrorCode = "ERR_MISSING_PODGANG_LABEL_ON_PODCLIQUE"
	errCodeInitContainerImageEnvVarMissing     grovecorev1alpha1.ErrorCode = "ERR_INITCONTAINER_ENVIRONMENT_VARIABLE_MISSING"
	errCodeCreatePodCliqueExpectationsStoreKey grovecorev1alpha1.ErrorCode = "ERR_CREATE_PODCLIQUE_EXPECTATIONS_STORE_KEY"
	errCodeDeletePodCliqueExpectations         grovecorev1alpha1.ErrorCode = "ERR_DELETE_PODCLIQUE_EXPECTATIONS_STORE_KEY"
	errCodeGetPodCliqueSetReplicaIndex         grovecorev1alpha1.ErrorCode = "ERR_GET_PODCLIQUESET_REPLICA_INDEX"
	errCodeSetControllerReference              grovecorev1alpha1.ErrorCode = "ERR_SET_CONTROLLER_REFERENCE"
	errCodeBuildPodResource                    grovecorev1alpha1.ErrorCode = "ERR_BUILD_POD_RESOURCE"
	errCodeMissingPodCliqueTemplate            grovecorev1alpha1.ErrorCode = "ERR_MISSING_PODCLIQUE_TEMPLATE"
	errCodeGetPodCliqueTemplate                grovecorev1alpha1.ErrorCode = "ERR_GET_PODCLIQUE_TEMPLATE"
	errCodeUpdatePodCliqueStatus               grovecorev1alpha1.ErrorCode = "ERR_UPDATE_PODCLIQUE_STATUS"
)

const (
	podGangSchedulingGate = "grove.io/podgang-pending-creation"
)

type _resource struct {
	client            client.Client
	scheme            *runtime.Scheme
	eventRecorder     record.EventRecorder
	expectationsStore *expect.ExpectationsStore
}

// New creates a new Pod operator for managing Pod resources within PodCliques
func New(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, expectationsStore *expect.ExpectationsStore) component.Operator[grovecorev1alpha1.PodClique] {
	return &_resource{
		client:            client,
		scheme:            scheme,
		eventRecorder:     eventRecorder,
		expectationsStore: expectationsStore,
	}
}

// GetExistingResourceNames returns the names of all the existing pods for the given PodClique.
// NOTE: Since we do not currently support Jobs, therefore we do not have to filter the pods that are reached their final state.
// Pods created for Jobs can reach corev1.PodSucceeded state or corev1.PodFailed state but these are not relevant for us at the moment.
// In future when these states become relevant then we have to list the pods and filter on their status.Phase.
func (r _resource) GetExistingResourceNames(ctx context.Context, _ logr.Logger, pclqObjMeta metav1.ObjectMeta) ([]string, error) {
	var podNames []string
	objMetaList := &metav1.PartialObjectMetadataList{}
	objMetaList.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Pod"))
	if err := r.client.List(ctx,
		objMetaList,
		client.InNamespace(pclqObjMeta.Namespace),
		client.MatchingLabels(getSelectorLabelsForPods(pclqObjMeta)),
	); err != nil {
		return podNames, groveerr.WrapError(err,
			errCodeGetPod,
			component.OperationGetExistingResourceNames,
			"failed to list pods",
		)
	}
	for _, pod := range objMetaList.Items {
		if metav1.IsControlledBy(&pod, &pclqObjMeta) {
			podNames = append(podNames, pod.Name)
		}
	}
	return podNames, nil
}

// Sync ensures that the desired number of Pods exist for the PodClique with the correct configuration
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pclq *grovecorev1alpha1.PodClique) error {
	sc, err := r.prepareSyncFlow(ctx, logger, pclq)
	if err != nil {
		return err
	}
	result := r.runSyncFlow(logger, sc)
	if result.hasErrors() {
		return result.getAggregatedError()
	}
	if result.hasPendingScheduleGatedPods() {
		return groveerr.New(groveerr.ErrCodeRequeueAfter,
			component.OperationSync,
			"some pods are still schedule gated. requeuing request to retry removal of scheduling gates",
		)
	}
	return nil
}

// buildResource constructs a Pod resource from PodClique specifications, setting up metadata, labels, scheduling gates, and dependencies
func (r _resource) buildResource(pcs *grovecorev1alpha1.PodCliqueSet, pclq *grovecorev1alpha1.PodClique, podGangName string, pod *corev1.Pod, podIndex int) error {
	// Extract PCS replica index from PodClique name for now (will be replaced with direct parameter)
	pcsName := componentutils.GetPodCliqueSetName(pclq.ObjectMeta)
	pcsReplicaIndex, err := utils.GetPodCliqueSetReplicaIndexFromPodCliqueFQN(pcsName, pclq.Name)
	if err != nil {
		return groveerr.WrapError(err,
			errCodeGetPodCliqueSetReplicaIndex,
			component.OperationSync,
			fmt.Sprintf("error extracting PCS replica index for PodClique %v", client.ObjectKeyFromObject(pclq)),
		)
	}

	labels := getLabels(pclq.ObjectMeta, pcsName, podGangName, pcsReplicaIndex)
	pod.ObjectMeta = metav1.ObjectMeta{
		GenerateName: fmt.Sprintf("%s-", pclq.Name),
		Namespace:    pclq.Namespace,
		Labels:       labels,
		Annotations:  pclq.Annotations,
	}
	if err = controllerutil.SetControllerReference(pclq, pod, r.scheme); err != nil {
		return groveerr.WrapError(err,
			errCodeSetControllerReference,
			component.OperationSync,
			fmt.Sprintf("error setting controller reference of PodClique: %v on Pod", client.ObjectKeyFromObject(pclq)),
		)
	}
	pod.Spec = *pclq.Spec.PodSpec.DeepCopy()
	pod.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: podGangSchedulingGate}}
	// Add GROVE specific Pod environment variables
	addEnvironmentVariables(pod, pclq, pcsName, pcsReplicaIndex, podIndex)
	// Configure hostname and subdomain for service discovery
	configurePodHostname(pcsName, pcsReplicaIndex, pclq.Name, pod, podIndex)
	// If there is a need to enforce a Startup-Order then configure the init container and add it to the Pod Spec.
	if len(pclq.Spec.StartsAfter) != 0 {
		return configurePodInitContainer(pcs, pclq, pod)
	}
	return nil
}

// Delete removes all Pods associated with the specified PodClique
func (r _resource) Delete(ctx context.Context, logger logr.Logger, pclqObjectMeta metav1.ObjectMeta) error {
	logger.Info("Triggering delete of all pods for the PodClique")
	if err := r.client.DeleteAllOf(ctx,
		&corev1.Pod{},
		client.InNamespace(pclqObjectMeta.Namespace),
		client.MatchingLabels(getSelectorLabelsForPods(pclqObjectMeta))); err != nil {
		return groveerr.WrapError(err,
			errCodeDeletePod,
			component.OperationDelete,
			fmt.Sprintf("failed to delete all pods for PodClique %v", k8sutils.GetObjectKeyFromObjectMeta(pclqObjectMeta)),
		)
	}
	pclqExpStoreKey, err := getPodCliqueExpectationsStoreKey(logger, component.OperationDelete, pclqObjectMeta)
	if err != nil {
		return err
	}
	if err = r.expectationsStore.DeleteExpectations(logger, pclqExpStoreKey); err != nil {
		return groveerr.WrapError(err,
			errCodeDeletePodCliqueExpectations,
			component.OperationDelete,
			fmt.Sprintf("failed to delete expectations store for PodClique %v", pclqObjectMeta.Name))
	}
	logger.Info("Successfully deleted all pods for the PodClique")
	return nil
}

// getSelectorLabelsForPods creates label selector map for identifying pods belonging to a PodClique.
// NOTE: We must get the PCS name from labels, not from owner reference.
// For PCSG-owned PodCliques, the owner is the PodCliqueScalingGroup (not PodCliqueSet),
// but pods are always labeled with the PCS name via LabelPartOfKey.
// Using GetFirstOwnerName() would return the PCSG name, causing a label mismatch
// that prevents pods from being deleted during PodClique cleanup.
func getSelectorLabelsForPods(pclqObjectMeta metav1.ObjectMeta) map[string]string {
	pcsName := pclqObjectMeta.Labels[apicommon.LabelPartOfKey]
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName),
		map[string]string{
			apicommon.LabelPodClique: pclqObjectMeta.Name,
		},
	)
}

// getLabels constructs the complete set of labels for a pod including Grove-specific and template labels
func getLabels(pclqObjectMeta metav1.ObjectMeta, pcsName, podGangName string, pcsReplicaIndex int) map[string]string {
	labels := map[string]string{
		apicommon.LabelPodClique:                pclqObjectMeta.Name,
		apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(pcsReplicaIndex),
		apicommon.LabelPodGang:                  podGangName,
	}
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName),
		pclqObjectMeta.Labels,
		labels,
	)
}

// addEnvironmentVariables adds Grove-specific environment variables to all containers and init-containers.
func addEnvironmentVariables(pod *corev1.Pod, pclq *grovecorev1alpha1.PodClique, pcsName string, pcsReplicaIndex, podIndex int) {
	groveEnvVars := []corev1.EnvVar{
		{
			Name:  constants.EnvVarPodCliqueSetName,
			Value: pcsName,
		},
		{
			Name:  constants.EnvVarPodCliqueSetIndex,
			Value: strconv.Itoa(pcsReplicaIndex),
		},
		{
			Name:  constants.EnvVarPodCliqueName,
			Value: pclq.Name,
		},
		{
			Name: constants.EnvVarHeadlessService,
			Value: apicommon.GenerateHeadlessServiceAddress(
				apicommon.ResourceNameReplica{Name: pcsName, Replica: pcsReplicaIndex},
				pod.Namespace),
		},
		{
			Name:  constants.EnvVarPodIndex,
			Value: strconv.Itoa(podIndex),
		},
	}
	componentutils.AddEnvVarsToContainers(pod.Spec.Containers, groveEnvVars)
	componentutils.AddEnvVarsToContainers(pod.Spec.InitContainers, groveEnvVars)
}

// configurePodHostname sets the pod hostname and subdomain for service discovery
func configurePodHostname(pcsName string, pcsReplicaIndex int, pclqName string, pod *corev1.Pod, podIndex int) {
	// Set hostname for service discovery (e.g., "my-pclq-0")
	pod.Spec.Hostname = fmt.Sprintf("%s-%d", pclqName, podIndex)

	// Set subdomain to headless service name (reusing existing logic)
	pod.Spec.Subdomain = apicommon.GenerateHeadlessServiceName(
		apicommon.ResourceNameReplica{Name: pcsName, Replica: pcsReplicaIndex})
}
