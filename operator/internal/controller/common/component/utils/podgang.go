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
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/samber/lo"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// ArePodGangsReady returns true when every named PodGang exists in the given namespace
// and reports PodGangConditionTypeReady=True. Returns false (with a nil error) if any PodGang
// is not found or has not yet reached Ready. Returns an error only on unexpected API
// failures (anything other than NotFound).
func ArePodGangsReady(ctx context.Context, cl client.Client, namespace string, names []string) (bool, error) {
	for _, name := range names {
		pg, err := GetPodGang(ctx, cl, name, namespace)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		if !k8sutils.IsConditionTrue(pg.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeReady)) {
			return false, nil
		}
	}
	return true, nil
}

// AllPodGangsAtEpochEverScheduled returns true when every PodGang created for
// a PodCliqueSet replica index and epoch has been scheduled at least once.
// The check is monotonic: once a PodGang has ever been scheduled, a subsequent
// flap of PodGangConditionTypeScheduled tp `False` does not undo the truth of this predicate.
// It returns false when no PodGang matches the (pcsName, pcsReplicaIndex, epoch) triple.
// Returns (_, err) only on transport errors from the client list.
func AllPodGangsAtEpochEverScheduled(ctx context.Context,
	cl client.Client,
	namespace string,
	pcsName string,
	pcsReplicaIndex int32,
	epoch string) (bool, error) {

	var podGangs groveschedulerv1alpha1.PodGangList
	if err := cl.List(ctx, &podGangs, client.InNamespace(namespace), client.MatchingLabels(map[string]string{
		apicommon.LabelPartOfKey:                pcsName,
		apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(int(pcsReplicaIndex)),
		apicommon.LabelEpoch:                    epoch,
	})); err != nil {
		return false, err
	}
	if len(podGangs.Items) == 0 {
		return false, nil
	}
	for i := range podGangs.Items {
		podGang := podGangs.Items[i]
		if podGang.Status.LastScheduled == nil {
			return false, nil
		}
	}
	return true, nil
}

// AllPodGangsAtEpochEverReady returns true when every PodGang created for
// a PodCliqueSet replica index and epoch has become ready at least once.
// The check is monotonic: once a PodGang has ever been ready, a subsequent
// flap of PodGangConditionTypeReady tp `False` does not undo the truth of this predicate.
// It returns false when no PodGang matches the (pcsName, pcsReplicaIndex, epoch) triple.
// Returns (_, err) only on transport errors from the client list.
func AllPodGangsAtEpochEverReady(ctx context.Context,
	cl client.Client,
	namespace string,
	pcsName string,
	pcsReplicaIndex int32,
	epoch string) (bool, error) {

	var podGangs groveschedulerv1alpha1.PodGangList
	if err := cl.List(ctx, &podGangs, client.InNamespace(namespace), client.MatchingLabels(map[string]string{
		apicommon.LabelPartOfKey:                pcsName,
		apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(int(pcsReplicaIndex)),
		apicommon.LabelEpoch:                    epoch,
	})); err != nil {
		return false, err
	}
	if len(podGangs.Items) == 0 {
		return false, nil
	}
	for i := range podGangs.Items {
		podGang := podGangs.Items[i]
		if podGang.Status.LastReady == nil {
			return false, nil
		}
	}
	return true, nil
}
