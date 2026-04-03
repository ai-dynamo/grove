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

package utils

import (
	"context"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetPodGangSelectorLabels creates the label selector to list all the PodGangs for a PodCliqueSet.
func GetPodGangSelectorLabels(pcsObjMeta metav1.ObjectMeta) map[string]string {
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsObjMeta.Name),
		map[string]string{
			apicommon.LabelComponentKey: apicommon.LabelComponentNamePodGang,
		})
}

// GetPodGang fetches a PodGang by name and namespace.
func GetPodGang(ctx context.Context, cl client.Client, podGangName, namespace string) (*groveschedulerv1alpha1.PodGang, error) {
	podGang := &groveschedulerv1alpha1.PodGang{}
	podGangObjectKey := client.ObjectKey{Namespace: namespace, Name: podGangName}
	if err := cl.Get(ctx, podGangObjectKey, podGang); err != nil {
		return nil, err
	}
	return podGang, nil
}

// GetExistingPodGangs fetches all existing PodGangs that are managed by Grove in the given namespace.
func GetExistingPodGangs(ctx context.Context, cl client.Client, pcsObjectMeta metav1.ObjectMeta, namespace string) ([]groveschedulerv1alpha1.PodGang, error) {
	podGangs := groveschedulerv1alpha1.PodGangList{}
	if err := cl.List(ctx, &podGangs,
		client.InNamespace(namespace),
		client.MatchingLabels(GetPodGangSelectorLabels(pcsObjectMeta))); err != nil {
		return nil, err
	}
	return podGangs.Items, nil
}
