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

package resourceclaim

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// ResourceSharer is the common interface for all level-specific resource sharing
// types (PCS, PCSG, PCLQ). It enables the reconciler to operate uniformly over
// different resource sharing specs regardless of their filter type.
type ResourceSharer interface {
	GetBase() *grovecorev1alpha1.ResourceSharingSpec
	FilterMatches(matchNames ...string) bool
}

// ResourceSharersFromPCS converts PCS-level specs to the common ResourceSharer interface.
func ResourceSharersFromPCS(specs []grovecorev1alpha1.PCSResourceSharingSpec) []ResourceSharer {
	s := make([]ResourceSharer, len(specs))
	for i := range specs {
		s[i] = &specs[i]
	}
	return s
}

// ResourceSharersFromPCSG converts PCSG-level specs to the common ResourceSharer interface.
func ResourceSharersFromPCSG(specs []grovecorev1alpha1.PCSGResourceSharingSpec) []ResourceSharer {
	s := make([]ResourceSharer, len(specs))
	for i := range specs {
		s[i] = &specs[i]
	}
	return s
}

// ResourceSharersFromPCLQ converts PCLQ-level specs to the common ResourceSharer interface.
func ResourceSharersFromPCLQ(specs []grovecorev1alpha1.ResourceSharingSpec) []ResourceSharer {
	s := make([]ResourceSharer, len(specs))
	for i := range specs {
		s[i] = &specs[i]
	}
	return s
}

// EnsureResourceClaim ensures a ResourceClaim exists with the given spec, labels, and owner.
// ResourceClaim.spec is immutable in Kubernetes, so existing claims only get metadata updates.
func EnsureResourceClaim(
	ctx context.Context,
	cl client.Client,
	name, namespace string,
	spec *resourcev1.ResourceClaimTemplateSpec,
	labels map[string]string,
	owner metav1.Object,
	scheme *runtime.Scheme,
) error {
	rc := &resourcev1.ResourceClaim{}
	err := cl.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, rc)
	if apierrors.IsNotFound(err) {
		rc = &resourcev1.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels:    labels,
			},
			Spec: spec.Spec,
		}
		if err := controllerutil.SetControllerReference(owner, rc, scheme); err != nil {
			return err
		}
		createErr := cl.Create(ctx, rc)
		if createErr == nil {
			return nil
		}
		if !apierrors.IsAlreadyExists(createErr) {
			return createErr
		}
		// Stale cache: the RC was created between our Get and Create.
		// Re-fetch and fall through to the update path below.
		if err := cl.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, rc); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	// RC already exists — only update metadata (labels, owner reference).
	// ResourceClaim.spec is immutable in Kubernetes; never attempt to patch it.
	rc.Labels = lo.Assign(rc.Labels, labels)
	if err := controllerutil.SetControllerReference(owner, rc, scheme); err != nil {
		return err
	}
	return cl.Update(ctx, rc)
}

// DeleteResourceClaim deletes a ResourceClaim by name. NotFound errors are ignored.
func DeleteResourceClaim(ctx context.Context, cl client.Client, name, namespace string) error {
	rc := &resourcev1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
	return client.IgnoreNotFound(cl.Delete(ctx, rc))
}

// EnsureResourceClaims creates ResourceClaims for a list of ResourceSharer entries
// at a given level. It resolves each ref, generates the deterministic name, and creates the RC.
// Errors are collected and returned as a single aggregated error.
func EnsureResourceClaims(
	ctx context.Context,
	cl client.Client,
	ownerName, namespace string,
	resourceSharers []ResourceSharer,
	pcsTemplates []grovecorev1alpha1.ResourceClaimTemplateConfig,
	labels map[string]string,
	owner metav1.Object,
	scheme *runtime.Scheme,
	replicaIndex *int, // nil for AllReplicas scope; set for PerReplica scope filtering
) error {
	var errs []error
	for i, s := range resourceSharers {
		base := s.GetBase()
		if replicaIndex == nil && base.Scope != grovecorev1alpha1.ResourceSharingScopeAllReplicas {
			continue
		}
		if replicaIndex != nil && base.Scope != grovecorev1alpha1.ResourceSharingScopePerReplica {
			continue
		}

		spec, err := ResolveTemplateSpec(ctx, cl, base, pcsTemplates, namespace)
		if err != nil {
			errs = append(errs, fmt.Errorf("ref %q (index %d): %w", base.Name, i, err))
			continue
		}

		rcName := RCName(ownerName, base, replicaIndex)

		if err := EnsureResourceClaim(ctx, cl, rcName, namespace, spec, labels, owner, scheme); err != nil {
			errs = append(errs, fmt.Errorf("RC %q: %w", rcName, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("failed to ensure ResourceClaims: %w", errors.Join(errs...))
	}
	return nil
}

// ResourceClaimLabels returns the standard Grove labels for a ResourceClaim owned by a PCS.
func ResourceClaimLabels(pcsName string) map[string]string {
	labels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName)
	labels[apicommon.LabelComponentKey] = apicommon.LabelComponentNameResourceClaim
	return labels
}

// RCName returns the deterministic ResourceClaim name for a given base ref and optional replica index.
// Callers must ensure replicaIndex is non-nil when base.Scope is PerReplica.
func RCName(ownerName string, base *grovecorev1alpha1.ResourceSharingSpec, replicaIndex *int) string {
	if base.Scope == grovecorev1alpha1.ResourceSharingScopeAllReplicas {
		return AllReplicasRCName(ownerName, base.Name)
	}
	return PerReplicaRCName(ownerName, *replicaIndex, base.Name)
}

// InjectResourceClaimRefs appends ResourceClaim references to a PodSpec for entries
// that match the given scope and filter. It adds both the pod-level claim
// (spec.resourceClaims) and the container-level claim reference
// (containers[].resources.claims) for every container in the pod so that all
// containers can access the allocated devices.
//
// matchNames are the names to match against the Filter (e.g. PCLQ template name,
// PCSG config name). When no matchNames are provided, filtering is skipped (for
// PCLQ-level refs where there is no child filtering).
func InjectResourceClaimRefs(
	podSpec *corev1.PodSpec,
	ownerName string,
	resourceSharers []ResourceSharer,
	replicaIndex *int, // nil = inject AllReplicas-scope RCs; non-nil = inject PerReplica for this replica
	matchNames ...string,
) {
	for _, s := range resourceSharers {
		if !s.FilterMatches(matchNames...) {
			continue
		}

		base := s.GetBase()
		if replicaIndex == nil && base.Scope != grovecorev1alpha1.ResourceSharingScopeAllReplicas {
			continue
		}
		if replicaIndex != nil && base.Scope != grovecorev1alpha1.ResourceSharingScopePerReplica {
			continue
		}

		rcName := RCName(ownerName, base, replicaIndex)

		podSpec.ResourceClaims = append(podSpec.ResourceClaims, corev1.PodResourceClaim{
			Name:              rcName,
			ResourceClaimName: &rcName,
		})

		containerClaim := corev1.ResourceClaim{Name: rcName}
		for ci := range podSpec.Containers {
			podSpec.Containers[ci].Resources.Claims = append(
				podSpec.Containers[ci].Resources.Claims, containerClaim)
		}
		for ci := range podSpec.InitContainers {
			podSpec.InitContainers[ci].Resources.Claims = append(
				podSpec.InitContainers[ci].Resources.Claims, containerClaim)
		}
	}
}

// FindPCSGConfig finds the matching PodCliqueScalingGroupConfig for a given PCSG from the PCS template.
func FindPCSGConfig(
	pcs *grovecorev1alpha1.PodCliqueSet,
	pcsg *grovecorev1alpha1.PodCliqueScalingGroup,
	pcsReplicaIndex int,
) *grovecorev1alpha1.PodCliqueScalingGroupConfig {
	return FindPCSGConfigByName(pcs, pcsg.Name, pcsReplicaIndex)
}

// FindPCSGConfigByName finds the matching PodCliqueScalingGroupConfig by PCSG name
// without requiring the full PCSG object. This is useful in contexts (e.g. the pod
// component) where only the PCSG name is available from labels.
func FindPCSGConfigByName(
	pcs *grovecorev1alpha1.PodCliqueSet,
	pcsgName string,
	pcsReplicaIndex int,
) *grovecorev1alpha1.PodCliqueScalingGroupConfig {
	pcsNameReplica := apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}
	for i := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		cfg := &pcs.Spec.Template.PodCliqueScalingGroupConfigs[i]
		if apicommon.GeneratePodCliqueScalingGroupName(pcsNameReplica, cfg.Name) == pcsgName {
			return cfg
		}
	}
	return nil
}

// DeletePerReplicaRCs deletes all PerReplica-scoped ResourceClaims for a given replica index.
func DeletePerReplicaRCs(
	ctx context.Context,
	cl client.Client,
	ownerName, namespace string,
	resourceSharers []ResourceSharer,
	replicaIndex int,
) error {
	var errs []error
	for _, s := range resourceSharers {
		base := s.GetBase()
		if base.Scope != grovecorev1alpha1.ResourceSharingScopePerReplica {
			continue
		}
		rcName := PerReplicaRCName(ownerName, replicaIndex, base.Name)
		if err := DeleteResourceClaim(ctx, cl, rcName, namespace); err != nil {
			errs = append(errs, fmt.Errorf("delete RC %q: %w", rcName, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("failed to delete PerReplica RCs: %w", errors.Join(errs...))
	}
	return nil
}

// CleanupStalePerReplicaRCs deletes stale PerReplica ResourceClaims in a single
// server-side DeleteCollection call. It builds a unified label selector that
// combines the matchLabels equality requirements with Exists + NotIn on the
// given replicaIndexLabel to target only RCs whose replica index >= currentReplicas.
// All requirements are merged into a single MatchingLabelsSelector to avoid
// issues with MatchingLabels + MatchingLabelsSelector option merging in
// controller-runtime's DeleteAllOf / List.
//
// NOTE: The NotIn requirement enumerates all valid replica indices [0..currentReplicas-1],
// so the selector size grows linearly with replica count. This is acceptable at
// current scale but worth noting for very large replica counts.
func CleanupStalePerReplicaRCs(
	ctx context.Context,
	cl client.Client,
	namespace string,
	matchLabels map[string]string,
	currentReplicas int,
	replicaIndexLabel string,
) error {
	// Build a single unified selector that includes both the matchLabels
	// equality requirements and the stale-replica set-based requirements.
	sel := labels.SelectorFromValidatedSet(labels.Set(matchLabels))
	existsReq, err := labels.NewRequirement(replicaIndexLabel, selection.Exists, nil)
	if err != nil {
		return fmt.Errorf("build Exists requirement: %w", err)
	}
	sel = sel.Add(*existsReq)

	if currentReplicas > 0 {
		validIndices := make([]string, currentReplicas)
		for i := range currentReplicas {
			validIndices[i] = strconv.Itoa(i)
		}
		notInReq, err := labels.NewRequirement(replicaIndexLabel, selection.NotIn, validIndices)
		if err != nil {
			return fmt.Errorf("build NotIn requirement: %w", err)
		}
		sel = sel.Add(*notInReq)
	}

	return cl.DeleteAllOf(ctx, &resourcev1.ResourceClaim{},
		client.InNamespace(namespace),
		client.MatchingLabelsSelector{Selector: sel},
	)
}
