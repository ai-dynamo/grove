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

package podgangmap

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	errCodeListPodGangMaps   grovecorev1alpha1.ErrorCode = "ERR_LIST_PODGANGMAPS"
	errCodeSyncPodGangMap    grovecorev1alpha1.ErrorCode = "ERR_SYNC_PODGANGMAP"
	errCodeDeletePodGangMaps grovecorev1alpha1.ErrorCode = "ERR_DELETE_PODGANGMAPS"
	errCodeListPCLQs         grovecorev1alpha1.ErrorCode = "ERR_LIST_PCLQS_FOR_PODGANGMAP"
)

type _resource struct {
	client client.Client
	scheme *runtime.Scheme
	clk    clock.Clock
}

// New creates a new instance of the PodGangMap component operator.
func New(cl client.Client, scheme *runtime.Scheme, clk clock.Clock) component.Operator[grovecorev1alpha1.PodCliqueSet] {
	return &_resource{
		client: cl,
		scheme: scheme,
		clk:    clk,
	}
}

// GetExistingResourceNames returns the names of existing PodGangMap resources owned by the PodCliqueSet.
func (r _resource) GetExistingResourceNames(ctx context.Context, _ logr.Logger, pcsObjMeta metav1.ObjectMeta) ([]string, error) {
	objMetaList := &metav1.PartialObjectMetadataList{}
	objMetaList.SetGroupVersionKind(grovecorev1alpha1.SchemeGroupVersion.WithKind("PodGangMap"))
	if err := r.client.List(ctx,
		objMetaList,
		client.InNamespace(pcsObjMeta.Namespace),
		client.MatchingLabels(getSelectorLabels(pcsObjMeta.Name)),
	); err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPodGangMaps,
			component.OperationGetExistingResourceNames,
			fmt.Sprintf("Error listing PodGangMap for PodCliqueSet: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	return k8sutils.FilterMapOwnedResourceNames(pcsObjMeta, objMetaList.Items), nil
}

// Sync creates or updates PodGangMap resources.
// PodGangMap is the single source of truth for PodGang composition in all cases.
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	logger.Info("Syncing PodGangMap resources")

	sc, err := r.prepareSyncFlow(ctx, logger, pcs)
	if err != nil {
		return err
	}
	return r.runSyncFlow(ctx, sc)
}

// Delete removes all PodGangMap resources owned by the PodCliqueSet.
func (r _resource) Delete(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) error {
	logger.Info("Triggering deletion of PodGangMaps")
	if err := r.client.DeleteAllOf(ctx,
		&grovecorev1alpha1.PodGangMap{},
		client.InNamespace(pcsObjMeta.Namespace),
		client.MatchingLabels(getSelectorLabels(pcsObjMeta.Name)),
	); err != nil {
		return groveerr.WrapError(err,
			errCodeDeletePodGangMaps,
			component.OperationDelete,
			fmt.Sprintf("Error deleting PodGangMaps for PodCliqueSet: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	logger.Info("Deleted PodGangMaps")
	return nil
}

// buildResource configures the PodGangMap with the desired entries.
func (r _resource) buildResource(pgm *grovecorev1alpha1.PodGangMap, pcs *grovecorev1alpha1.PodCliqueSet, replicaIndex int, entries []grovecorev1alpha1.PodGangEntry) error {
	if err := controllerutil.SetControllerReference(pcs, pgm, r.scheme); err != nil {
		return groveerr.WrapError(err,
			errCodeSyncPodGangMap,
			component.OperationSync,
			fmt.Sprintf("Error setting controller reference on PodGangMap %s", pgm.Name),
		)
	}
	pgm.Labels = getLabels(pcs.Name, replicaIndex)
	pgm.Spec.PodCliqueSetReplicaIndex = int32(replicaIndex)
	slices.SortFunc(entries, func(a, b grovecorev1alpha1.PodGangEntry) int {
		return strings.Compare(a.Name, b.Name)
	})
	pgm.Spec.Entries = entries
	return nil
}

// getSelectorLabels returns labels for selecting all PodGangMaps of a PodCliqueSet.
func getSelectorLabels(pcsName string) map[string]string {
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName),
		map[string]string{
			apicommon.LabelComponentKey: apicommon.LabelComponentNamePodGangMap,
		},
	)
}

// getLabels returns labels for a PodGangMap resource.
func getLabels(pcsName string, replicaIndex int) map[string]string {
	return lo.Assign(
		getSelectorLabels(pcsName),
		map[string]string{
			apicommon.LabelAppNameKey:               fmt.Sprintf("%s-%d", pcsName, replicaIndex),
			apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(replicaIndex),
		},
	)
}

// emptyPodGangMap creates an empty PodGangMap with only metadata set.
func emptyPodGangMap(objKey client.ObjectKey) *grovecorev1alpha1.PodGangMap {
	return &grovecorev1alpha1.PodGangMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objKey.Name,
			Namespace: objKey.Namespace,
		},
	}
}

// PodGangEntryBuilder is a function that creates a PodGangEntry given standalone PCLQ pod counts,
// PCSG replica indices for the PodGang, and the grove.io/epoch value this entry depends on (nil
// means no ordering constraint).
type PodGangEntryBuilder func(standalonePCLQReplicas map[string]int32, pcsgReplicaIndices map[string][]int32, dependsOn []string) grovecorev1alpha1.PodGangEntry

// NewPodGangEntryBuilder returns a closure that creates PodGangEntry values with names
// generated under the unified PodGang naming convention.
//
// The builder captures one epoch at construction (clock.Now().UnixNano()) and stamps every
// entry it produces with that epoch as the grove.io/epoch label. All entries from one builder
// therefore belong to the same batch. Names remain unique within a batch by salting the
// suffix with a within-builder counter (+i on every invocation).
//
// pcsGenerationHash is not part of the PodGang name; it is written into
// PodGangEntry.PodCliqueSetGenerationHash so consumers can identify the cohort each entry
// belongs to.
//
// In production callers should pass clock.RealClock{}; tests can pass clocktesting.FakeClock
// for deterministic name generation.
func NewPodGangEntryBuilder(pcsName string, pcsReplicaIndex int, pcsGenerationHash string, clock clock.Clock) PodGangEntryBuilder {
	var i int64
	baseEpoch := clock.Now().UnixNano()
	epochLabel := strconv.FormatInt(baseEpoch, 10)
	return func(standalonePCLQReplicas map[string]int32, pcsgReplicaIndices map[string][]int32, dependsOn []string) grovecorev1alpha1.PodGangEntry {
		suffix := strconv.FormatInt(baseEpoch+i, 10)
		i++
		return grovecorev1alpha1.PodGangEntry{
			Name:                       apicommon.GeneratePodGangName(pcsName, pcsReplicaIndex, suffix),
			PodCliqueSetGenerationHash: pcsGenerationHash,
			PodCliques:                 standalonePCLQReplicas,
			PCSGReplicaIndices:         pcsgReplicaIndices,
			DependsOn:                  dependsOn,
			Labels:                     map[string]string{apicommon.LabelEpoch: epochLabel},
		}
	}
}
