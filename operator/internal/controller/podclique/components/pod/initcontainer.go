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
	"fmt"
	"os"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	groveversion "github.com/ai-dynamo/grove/operator/internal/version"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
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

// configurePodInitContainer adds the necessary volumes and init container to the pod for dependency management
func configurePodInitContainer(pcs *grovecorev1alpha1.PodCliqueSet, pclq *grovecorev1alpha1.PodClique, pod *corev1.Pod) error {
	addServiceAccountTokenSecretVolume(pcs.Name, pod)
	addPodInfoVolume(pod)
	return addInitContainer(pcs, pclq, pod)
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

// addInitContainer adds the Grove init container to the pod with appropriate image, args, and volume mounts
func addInitContainer(pcs *grovecorev1alpha1.PodCliqueSet, pclq *grovecorev1alpha1.PodClique, pod *corev1.Pod) error {
	image, err := getInitContainerImage()
	if err != nil {
		return err
	}
	args, err := generateArgsForInitContainer(pcs, pclq)
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

// generateArgsForInitContainer creates command line arguments for the init container based on PodClique dependencies
func generateArgsForInitContainer(pcs *grovecorev1alpha1.PodCliqueSet, pclq *grovecorev1alpha1.PodClique) ([]string, error) {
	args := make([]string, 0)
	for _, parentCliqueFQN := range pclq.Spec.StartsAfter {
		parentCliqueTemplateSpec, ok := lo.Find(pcs.Spec.Template.Cliques, func(templateSpec *grovecorev1alpha1.PodCliqueTemplateSpec) bool {
			return strings.HasSuffix(parentCliqueFQN, templateSpec.Name)
		})
		if !ok {
			return nil, groveerr.New(
				errCodeMissingPodCliqueTemplate,
				component.OperationSync,
				fmt.Sprintf("PodClique %s specified in startsAfter is not present in the templates", parentCliqueFQN),
			)
		}
		args = append(args, fmt.Sprintf("--podcliques=%s:%d", parentCliqueFQN, *parentCliqueTemplateSpec.Spec.MinAvailable))
	}
	return args, nil
}
