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

package component

import (
	"context"
	"fmt"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetHeadlessServicesByPCSReplicaIndex lists headless Services controlled by the PCS and groups them by PCS replica index.
func GetHeadlessServicesByPCSReplicaIndex(ctx context.Context, cl client.Client, pcsObjectMeta metav1.ObjectMeta) (map[int][]corev1.Service, error) {
	services, err := listHeadlessServicesForPCS(ctx, cl, pcsObjectMeta)
	if err != nil {
		return nil, err
	}
	return groupHeadlessServicesByPCSReplicaIndex(services)
}

func listHeadlessServicesForPCS(ctx context.Context, cl client.Client, pcsObjectMeta metav1.ObjectMeta) ([]corev1.Service, error) {
	serviceList := &corev1.ServiceList{}
	selectorLabels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsObjectMeta.Name)
	selectorLabels[apicommon.LabelComponentKey] = apicommon.LabelComponentNamePodCliqueSetReplicaHeadlessService
	if err := cl.List(ctx, serviceList, client.InNamespace(pcsObjectMeta.Namespace), client.MatchingLabels(selectorLabels)); err != nil {
		return nil, err
	}

	services := make([]corev1.Service, 0, len(serviceList.Items))
	for _, service := range serviceList.Items {
		if metav1.IsControlledBy(&service, &pcsObjectMeta) {
			services = append(services, service)
		}
	}
	return services, nil
}

func groupHeadlessServicesByPCSReplicaIndex(services []corev1.Service) (map[int][]corev1.Service, error) {
	servicesByPCSReplicaIndex := make(map[int][]corev1.Service)
	for _, service := range services {
		pcsReplicaIndexValue, exists := service.Labels[apicommon.LabelPodCliqueSetReplicaIndex]
		if !exists {
			return nil, fmt.Errorf("headless Service %s has no %s label", client.ObjectKeyFromObject(&service), apicommon.LabelPodCliqueSetReplicaIndex)
		}
		pcsReplicaIndex, err := strconv.Atoi(pcsReplicaIndexValue)
		if err != nil {
			return nil, fmt.Errorf("%s label on headless Service %s is not a valid integer: %q", apicommon.LabelPodCliqueSetReplicaIndex, client.ObjectKeyFromObject(&service), pcsReplicaIndexValue)
		}
		servicesByPCSReplicaIndex[pcsReplicaIndex] = append(servicesByPCSReplicaIndex[pcsReplicaIndex], service)
	}
	return servicesByPCSReplicaIndex, nil
}
