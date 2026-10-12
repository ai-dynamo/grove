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

package pod

import (
	"fmt"
	"os"
	"slices"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	groveversion "github.com/ai-dynamo/grove/operator/internal/version"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// envVarInitContainerImage stores the environment variable which is read to find the image for the init-container.
	// The environment variable should only store the registry and repository of the init-container. It should not contain any tag.
	envVarInitContainerImage string = "GROVE_INIT_CONTAINER_IMAGE"
	// initContainerName is the name of the init container.
	initContainerName = "grove-initc"
	// serviceAccountTokenSecretVolumeName is the name of the volume that mounts the service account token secret.
	serviceAccountTokenSecretVolumeName = "sa-token-secret-vol"
	// podInfoVolumeName is the name of the downwardAPI volume that passes the pod information to the init container.
	podInfoVolumeName = "pod-info-vol"
	// volumeMountPathServiceAccount is the base path where token and CA.cert for the service account will be placed.
	volumeMountPathServiceAccount = "/var/run/secrets/kubernetes.io/serviceaccount"
)

// configurePodInitContainer adds the volumes and the startup-order init container to the pod. args are the
// resolved wait targets the init container blocks on.
func configurePodInitContainer(pcsName string, pod *corev1.Pod, args []string) error {
	addServiceAccountTokenSecretVolume(pcsName, pod)
	addPodInfoVolume(pod)
	return addInitContainer(pod, args)
}

// addServiceAccountTokenSecretVolume adds a volume that mounts the service account token secret
func addServiceAccountTokenSecretVolume(pcsName string, pod *corev1.Pod) {
	saTokenSecretVol := corev1.Volume{
		Name: serviceAccountTokenSecretVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName:  apicommon.GenerateInitContainerSATokenSecretName(pcsName),
				DefaultMode: ptr.To[int32](420),
			},
		},
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, saTokenSecretVol)
}

// addPodInfoVolume adds a downwardAPI volume that exposes pod metadata to the init container
func addPodInfoVolume(pod *corev1.Pod) {
	podInfoVol := corev1.Volume{
		Name: podInfoVolumeName,
		VolumeSource: corev1.VolumeSource{
			DownwardAPI: &corev1.DownwardAPIVolumeSource{
				Items: []corev1.DownwardAPIVolumeFile{
					{
						Path: constants.PodNamespaceFileName,
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.namespace",
						},
					},
					{
						Path: constants.PodGangNameFileName,
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: fmt.Sprintf("metadata.labels['%s']", apicommon.LabelPodGang),
						},
					},
				},
			},
		},
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, podInfoVol)
}

// addInitContainer adds the Grove init container to the pod with the resolved wait args and volume mounts
func addInitContainer(pod *corev1.Pod, args []string) error {
	image, err := getInitContainerImage()
	if err != nil {
		return err
	}

	pod.Spec.InitContainers = append(pod.Spec.InitContainers, corev1.Container{
		Name:  initContainerName,
		Image: fmt.Sprintf("%s:%s", image, groveversion.New().GitVersion),
		Args:  args,
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      podInfoVolumeName,
				ReadOnly:  true,
				MountPath: constants.VolumeMountPathPodInfo,
			},
			{
				Name:      serviceAccountTokenSecretVolumeName,
				ReadOnly:  true,
				MountPath: volumeMountPathServiceAccount,
			},
		},
	})
	return nil
}

// getInitContainerImage retrieves the init container image from environment variables
func getInitContainerImage() (string, error) {
	initContainerImage, ok := os.LookupEnv(envVarInitContainerImage)
	if !ok {
		return "", groveerr.New(
			errCodeInitContainerImageEnvVarMissing,
			component.OperationSync,
			fmt.Sprintf("environment variable %s specifying the init-container image is missing", envVarInitContainerImage),
		)
	}
	return initContainerImage, nil
}

// generateArgsForInitContainer builds the init container wait arguments for the pod. It derives this
// PodClique's declared startup parents from the PodCliqueSet template and resolves them against the committed
// PodGangMap entry of the pod's own PodGang. Only parents committed in that PodGang are returned, each with
// the number of pods to wait on, so the pod waits only for the parents present in its own PodGang.
func generateArgsForInitContainer(pcs *grovecorev1alpha1.PodCliqueSet, pclq *grovecorev1alpha1.PodClique, pod *corev1.Pod, pgm *grovecorev1alpha1.PodGangMap) ([]string, error) {
	pcsName := componentutils.GetPodCliqueSetName(pclq.ObjectMeta)
	pcsReplicaIndex, err := componentutils.GetPodCliqueSetReplicaIndexFromPodCliqueFQN(pcsName, pclq.Name)
	if err != nil {
		return nil, groveerr.WrapError(err, errCodeGetPodCliqueSetReplicaIndex, component.OperationSync,
			fmt.Sprintf("error extracting PodCliqueSet replica index for PodClique %v", client.ObjectKeyFromObject(pclq)))
	}
	rnr := apicommon.ResourceNameReplica{Name: pcsName, Replica: pcsReplicaIndex}
	parentCliqueNames, err := componentutils.StartupDependencyCliqueNames(pcs, cliqueTemplateName(pclq, rnr))
	if err != nil {
		return nil, groveerr.WrapError(err, errCodeResolveStartupDependencies, component.OperationSync,
			fmt.Sprintf("error identifying startup dependencies for PodClique %v", client.ObjectKeyFromObject(pclq)))
	}
	if len(parentCliqueNames) == 0 {
		return nil, nil
	}
	podGangName := pod.Labels[apicommon.LabelPodGang]
	entry := entryForPodGangName(pgm, rnr, podGangName)
	if entry == nil {
		return nil, groveerr.New(groveerr.ErrCodeRequeueAfter, component.OperationSync,
			fmt.Sprintf("PodGang %q for PodClique %v has no committed PodGangMap entry yet, requeuing", podGangName, client.ObjectKeyFromObject(pclq)))
	}
	args := make([]string, 0, len(parentCliqueNames))
	for _, target := range componentutils.StartupDependencyTargetsInEntry(pcs, pcsReplicaIndex, entry, podGangName, parentCliqueNames) {
		args = append(args, fmt.Sprintf("--podcliques=%s:%d", target.PodCliqueFQN, target.MinReady))
	}
	return args, nil
}

// cliqueTemplateName returns the clique template name of pclq. A PodCliqueScalingGroup member clique is named
// <pcsgFQN>-<pcsgReplicaIndex>-<cliqueName>, so the clique template name is recovered from the scaling group FQN and
// replica-index labels. A standalone PodClique is named <pcs>-<pcsReplicaIndex>-<cliqueName> and can be extracted
// given the PCS name and PCS replica index.
func cliqueTemplateName(pclq *grovecorev1alpha1.PodClique, pcsRnr apicommon.ResourceNameReplica) string {
	if pcsgFQN, ok := pclq.Labels[apicommon.LabelPodCliqueScalingGroup]; ok {
		prefix := fmt.Sprintf("%s-%s-", pcsgFQN, pclq.Labels[apicommon.LabelPodCliqueScalingGroupReplicaIndex])
		return strings.TrimPrefix(pclq.Name, prefix)
	}
	return apicommon.ExtractPodCliqueNameFromStandalonePCLQFQN(pclq.Name, pcsRnr)
}

// entryForPodGangName returns the committed PodGangMap entry that materializes podGangName for the replica,
// or nil when no entry does.
func entryForPodGangName(pgm *grovecorev1alpha1.PodGangMap, rnr apicommon.ResourceNameReplica, podGangName string) *grovecorev1alpha1.PodGangEntry {
	if pgm == nil {
		return nil
	}
	for i := range pgm.Spec.Entries {
		if slices.Contains(componentutils.ExpectedPodGangNamesForEntry(rnr, pgm.Spec.Entries[i]), podGangName) {
			return &pgm.Spec.Entries[i]
		}
	}
	return nil
}
