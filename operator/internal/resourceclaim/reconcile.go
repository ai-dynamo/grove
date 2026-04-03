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
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
)

// EnsureResourceClaim creates or patches a ResourceClaim with the given spec and owner.
func EnsureResourceClaim(
	ctx context.Context,
	cl client.Client,
	name, namespace string,
	spec *resourcev1.ResourceClaimTemplateSpec,
	owner metav1.Object,
	scheme *runtime.Scheme,
) error {
	rc := &resourcev1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	_, err := controllerutil.CreateOrPatch(ctx, cl, rc, func() error {
		rc.Spec = spec.Spec
		return controllerutil.SetControllerReference(owner, rc, scheme)
	})
	return err
}

// DeleteResourceClaim deletes a ResourceClaim by name. NotFound errors are ignored.
func DeleteResourceClaim(ctx context.Context, cl client.Client, name, namespace string) error {
	rc := &resourcev1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
	return client.IgnoreNotFound(cl.Delete(ctx, rc))
}

// EnsureResourceClaims creates ResourceClaims for a list of ResourceSharingSpec entries
// at a given level. It resolves each ref, generates the deterministic name, and creates the RC.
// Errors are collected and returned as a single aggregated error.
func EnsureResourceClaims(
	ctx context.Context,
	cl client.Client,
	reader client.Reader,
	ownerName, namespace string,
	refs []grovecorev1alpha1.ResourceSharingSpec,
	pcsTemplates []grovecorev1alpha1.ResourceClaimTemplateConfig,
	owner metav1.Object,
	scheme *runtime.Scheme,
	replicaIndex *int, // nil for AllReplicas scope; set for PerReplica scope filtering
) error {
	var errs []error
	for i := range refs {
		ref := &refs[i]
		if replicaIndex == nil && ref.Scope != grovecorev1alpha1.ResourceSharingScopeAllReplicas {
			continue
		}
		if replicaIndex != nil && ref.Scope != grovecorev1alpha1.ResourceSharingScopePerReplica {
			continue
		}

		spec, err := ResolveTemplateSpec(ctx, reader, ref, pcsTemplates, namespace)
		if err != nil {
			errs = append(errs, fmt.Errorf("ref %q (index %d): %w", ref.Name, i, err))
			continue
		}

		rcName := RCName(ownerName, ref, replicaIndex)

		if err := EnsureResourceClaim(ctx, cl, rcName, namespace, spec, owner, scheme); err != nil {
			errs = append(errs, fmt.Errorf("RC %q: %w", rcName, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("failed to ensure ResourceClaims: %v", errs)
	}
	return nil
}

// RCName returns the deterministic ResourceClaim name for a given ref and optional replica index.
func RCName(ownerName string, ref *grovecorev1alpha1.ResourceSharingSpec, replicaIndex *int) string {
	if ref.Scope == grovecorev1alpha1.ResourceSharingScopeAllReplicas {
		return AllReplicasRCName(ownerName, ref.Name)
	}
	return PerReplicaRCName(ownerName, *replicaIndex, ref.Name)
}

// filterMatches checks whether a ref's Filter allows injection for the given matchNames.
// When no matchNames are provided (PCLQ-level), filtering is skipped.
// When Filter is nil, all children match (broadcast).
// The filter is always include-based: only listed names receive the claims.
func filterMatches(ref *grovecorev1alpha1.ResourceSharingSpec, matchNames []string) bool {
	if len(matchNames) == 0 || ref.Filter == nil {
		return true
	}
	for _, name := range matchNames {
		if slices.Contains(ref.Filter.CliqueNames, name) || slices.Contains(ref.Filter.GroupNames, name) {
			return true
		}
	}
	return false
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
	refs []grovecorev1alpha1.ResourceSharingSpec,
	replicaIndex *int, // nil = inject AllReplicas-scope RCs; non-nil = inject PerReplica for this replica
	matchNames ...string,
) {
	for i := range refs {
		ref := &refs[i]

		if !filterMatches(ref, matchNames) {
			continue
		}

		if replicaIndex == nil && ref.Scope != grovecorev1alpha1.ResourceSharingScopeAllReplicas {
			continue
		}
		if replicaIndex != nil && ref.Scope != grovecorev1alpha1.ResourceSharingScopePerReplica {
			continue
		}

		rcName := RCName(ownerName, ref, replicaIndex)

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
	for i := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		cfg := &pcs.Spec.Template.PodCliqueScalingGroupConfigs[i]
		fqn := fmt.Sprintf("%s-%d-%s", pcs.Name, pcsReplicaIndex, cfg.Name)
		if fqn == pcsg.Name {
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
	refs []grovecorev1alpha1.ResourceSharingSpec,
	replicaIndex int,
) error {
	var errs []error
	for i := range refs {
		if refs[i].Scope != grovecorev1alpha1.ResourceSharingScopePerReplica {
			continue
		}
		rcName := PerReplicaRCName(ownerName, replicaIndex, refs[i].Name)
		if err := DeleteResourceClaim(ctx, cl, rcName, namespace); err != nil {
			errs = append(errs, fmt.Errorf("delete RC %q: %w", rcName, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("failed to delete PerReplica RCs: %v", errs)
	}
	return nil
}
