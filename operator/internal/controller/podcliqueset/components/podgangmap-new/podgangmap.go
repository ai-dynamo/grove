package podgangmap_new

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
	errCodeListPodGangMaps            grovecorev1alpha1.ErrorCode = "ERR_LIST_PODGANGMAPS"
	errCodeDeletePodGangMaps          grovecorev1alpha1.ErrorCode = "ERR_DELETE_PODGANGMAPS"
	errCodeCreateOrPatchPodGangMap    grovecorev1alpha1.ErrorCode = "ERR_CREATE_OR_PATCH_PODGANGMAP"
	errCodePatchPodGangMapStatus      grovecorev1alpha1.ErrorCode = "ERR_PATCH_PODGANGMAP_STATUS"
	errCodeSetControllerReference     grovecorev1alpha1.ErrorCode = "ERR_SET_CONTROLLER_REFERENCE"
	errCodeListPCLQs                  grovecorev1alpha1.ErrorCode = "ERR_LIST_PCLQS"
	errCodeGroupPCLQsByReplica        grovecorev1alpha1.ErrorCode = "ERR_GROUP_PCLQS_BY_REPLICA"
	errCodeListPCSGs                  grovecorev1alpha1.ErrorCode = "ERR_LIST_PCSGS"
	errCodeGroupPCSGsByReplica        grovecorev1alpha1.ErrorCode = "ERR_GROUP_PCSGS_BY_REPLICA"
	errCodeListPodGangs               grovecorev1alpha1.ErrorCode = "ERR_LIST_PODGANGS"
	errCodeComputeMVUTemplate         grovecorev1alpha1.ErrorCode = "ERR_COMPUTE_MVU_TEMPLATE"
	errCodeExtractPodCliqueName       grovecorev1alpha1.ErrorCode = "ERR_EXTRACT_PODCLIQUE_NAME"
	errCodeReconstructPodGangMapEntry grovecorev1alpha1.ErrorCode = "ERR_RECONSTRUCT_PODGANGMAP_ENTRY"
	errCodeMissingEpochLabel          grovecorev1alpha1.ErrorCode = "ERR_MISSING_EPOCH_LABEL"
	errCodeInvalidEpochLabel          grovecorev1alpha1.ErrorCode = "ERR_INVALID_EPOCH_LABEL"
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

// Sync reconciles the PodGangMap for every PCS replica. Takes a syncSnapshot
// once, synchronizes the PodGangMap resources in bootstrap, steady state and coherent update flows
// and cleans up orphan PodGangMap resources left by a PCS replica scale-in.
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	logger.V(4).Info("Syncing PodGangMap resources")
	syncSnap, err := r.takeSnapshot(ctx, logger, pcs)
	if err != nil {
		return err
	}
	return r.runSyncFlow(ctx, syncSnap)
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

// buildResource configures the PodGangMap with the desired entries.
func (r _resource) buildResource(pgm *grovecorev1alpha1.PodGangMap, pcs *grovecorev1alpha1.PodCliqueSet, replicaIndex int, entries []grovecorev1alpha1.PodGangEntry) error {
	if err := controllerutil.SetControllerReference(pcs, pgm, r.scheme); err != nil {
		return groveerr.WrapError(err,
			errCodeSetControllerReference,
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

// emptyPodGangMap creates an empty PodGangMap with only metadata set.
func emptyPodGangMap(objKey client.ObjectKey) *grovecorev1alpha1.PodGangMap {
	return &grovecorev1alpha1.PodGangMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objKey.Name,
			Namespace: objKey.Namespace,
		},
	}
}
