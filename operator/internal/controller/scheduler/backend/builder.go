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

package backend

import (
	"context"
	"fmt"
	"sort"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GangInfoBuilder builds PodGangInfo from PodCliqueSet and related resources
type GangInfoBuilder struct {
	client client.Client
}

// NewGangInfoBuilder creates a new gang info builder
func NewGangInfoBuilder(cl client.Client) *GangInfoBuilder {
	return &GangInfoBuilder{
		client: cl,
	}
}

// BuildGangInfos builds PodGangInfo for all replicas of a PodCliqueSet
func (b *GangInfoBuilder) BuildGangInfos(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) ([]*PodGangInfo, error) {
	// Get all PodCliques for this PodCliqueSet
	pclqs, err := b.listPodCliques(ctx, pcs)
	if err != nil {
		return nil, fmt.Errorf("failed to list PodCliques: %w", err)
	}

	// Get all Pods for this PodCliqueSet
	pods, err := b.listPods(ctx, pcs)
	if err != nil {
		return nil, fmt.Errorf("failed to list Pods: %w", err)
	}

	// Group PodCliques by PodCliqueSet replica index
	pclqsByReplica := b.groupPodCliquesByReplica(pcs.Name, pclqs)

	// Group Pods by PodGang name
	podsByPodGang := b.groupPodsByPodGang(pods)

	// Build PodGangInfo for each replica
	gangInfos := make([]*PodGangInfo, 0, len(pclqsByReplica))
	for replicaIndex := 0; replicaIndex < int(pcs.Spec.Replicas); replicaIndex++ {
		gangName := apicommon.GenerateBasePodGangName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: replicaIndex})
		
		gangInfo, err := b.buildGangInfoForReplica(
			pcs,
			gangName,
			replicaIndex,
			pclqsByReplica[replicaIndex],
			podsByPodGang[gangName],
		)
		if err != nil {
			return nil, fmt.Errorf("failed to build gang info for replica %d: %w", replicaIndex, err)
		}

		gangInfos = append(gangInfos, gangInfo)
	}

	return gangInfos, nil
}

// buildGangInfoForReplica builds PodGangInfo for a single replica
func (b *GangInfoBuilder) buildGangInfoForReplica(
	pcs *grovecorev1alpha1.PodCliqueSet,
	gangName string,
	replicaIndex int,
	pclqs []grovecorev1alpha1.PodClique,
	pods []corev1.Pod,
) (*PodGangInfo, error) {
	// Build pod groups from PodCliques
	podGroups := make([]PodGroupInfo, 0, len(pclqs))
	for _, pclq := range pclqs {
		// Get pods for this PodClique
		pclqPods := b.filterPodsByPodClique(pods, pclq.Name)
		
		podRefs := make([]NamespacedName, 0, len(pclqPods))
		for _, pod := range pclqPods {
			podRefs = append(podRefs, NamespacedName{
				Namespace: pod.Namespace,
				Name:      pod.Name,
			})
		}

		// Sort pod references for consistent ordering
		sort.Slice(podRefs, func(i, j int) bool {
			return podRefs[i].Name < podRefs[j].Name
		})

		minReplicas := int32(1)
		if pclq.Spec.MinAvailable != nil {
			minReplicas = *pclq.Spec.MinAvailable
		}
		
		podGroup := PodGroupInfo{
			Name:               pclq.Name,
			PodReferences:      podRefs,
			MinReplicas:        minReplicas,
			TopologyConstraint: convertTopologyConstraintFromPodClique(&pclq),
		}
		podGroups = append(podGroups, podGroup)
	}

	// Build topology constraint group configs from PCSGs
	topologyGroupConfigs := b.buildTopologyGroupConfigs(pcs, replicaIndex)

	gangInfo := &PodGangInfo{
		Name:                           gangName,
		Namespace:                      pcs.Namespace,
		PodGroups:                      podGroups,
		TopologyConstraint:             convertTopologyConstraintFromPCS(pcs),
		TopologyConstraintGroupConfigs: topologyGroupConfigs,
		PriorityClassName:              getPriorityClassName(pcs),
		ReuseReservationRef:            getReuseReservationRef(gangName, replicaIndex),
	}

	return gangInfo, nil
}

// listPodCliques lists all PodCliques for a PodCliqueSet
func (b *GangInfoBuilder) listPodCliques(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) ([]grovecorev1alpha1.PodClique, error) {
	pclqList := &grovecorev1alpha1.PodCliqueList{}
	
	// Get PodCliques belonging to this PodCliqueSet
	selector := labels.SelectorFromSet(labels.Set{
		apicommon.LabelPartOfKey: pcs.Name,
	})
	
	err := b.client.List(ctx, pclqList,
		client.InNamespace(pcs.Namespace),
		client.MatchingLabelsSelector{Selector: selector},
	)
	if err != nil {
		return nil, err
	}
	return pclqList.Items, nil
}

// listPods lists all Pods for a PodCliqueSet
func (b *GangInfoBuilder) listPods(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	
	// Use label selector to find pods belonging to this PodCliqueSet
	selector := labels.SelectorFromSet(labels.Set{
		apicommon.LabelPartOfKey: pcs.Name,
	})
	
	err := b.client.List(ctx, podList,
		client.InNamespace(pcs.Namespace),
		client.MatchingLabelsSelector{Selector: selector},
	)
	if err != nil {
		return nil, err
	}
	return podList.Items, nil
}

// groupPodCliquesByReplica groups PodCliques by replica index
func (b *GangInfoBuilder) groupPodCliquesByReplica(pcsName string, pclqs []grovecorev1alpha1.PodClique) map[int][]grovecorev1alpha1.PodClique {
	result := make(map[int][]grovecorev1alpha1.PodClique)
	
	for _, pclq := range pclqs {
		// Extract replica index from label
		replicaIndexStr, ok := pclq.Labels[apicommon.LabelPodCliqueSetReplicaIndex]
		if !ok {
			continue
		}
		
		var replicaIndex int
		if _, err := fmt.Sscanf(replicaIndexStr, "%d", &replicaIndex); err != nil {
			continue
		}
		
		result[replicaIndex] = append(result[replicaIndex], pclq)
	}
	
	return result
}

// groupPodsByPodGang groups Pods by PodGang name (from labels)
func (b *GangInfoBuilder) groupPodsByPodGang(pods []corev1.Pod) map[string][]corev1.Pod {
	result := make(map[string][]corev1.Pod)
	
	for _, pod := range pods {
		gangName := pod.Labels[apicommon.LabelPodGang]
		if gangName == "" {
			continue
		}
		result[gangName] = append(result[gangName], pod)
	}
	
	return result
}

// filterPodsByPodClique filters pods belonging to a specific PodClique
func (b *GangInfoBuilder) filterPodsByPodClique(pods []corev1.Pod, pclqName string) []corev1.Pod {
	result := make([]corev1.Pod, 0)
	for _, pod := range pods {
		if pod.Labels[apicommon.LabelPodClique] == pclqName {
			result = append(result, pod)
		}
	}
	return result
}

// buildTopologyGroupConfigs builds topology constraint group configs from PCSGs
func (b *GangInfoBuilder) buildTopologyGroupConfigs(pcs *grovecorev1alpha1.PodCliqueSet, replicaIndex int) []TopologyConstraintGroupConfig {
	// TODO: Implement PCSG topology constraints
	// This would iterate through PodCliqueScalingGroups and build topology configs
	return []TopologyConstraintGroupConfig{}
}

// convertTopologyConstraintFromPCS converts PCS topology constraint to backend format
func convertTopologyConstraintFromPCS(pcs *grovecorev1alpha1.PodCliqueSet) *TopologyConstraint {
	// TODO: Implement PCS-level topology constraint conversion
	return nil
}

// convertTopologyConstraintFromPodClique converts PodClique topology constraint to backend format
func convertTopologyConstraintFromPodClique(pclq *grovecorev1alpha1.PodClique) *TopologyConstraint {
	// TODO: Implement PodClique-level topology constraint conversion
	return nil
}

// getPriorityClassName extracts priority class name from PodCliqueSet
func getPriorityClassName(pcs *grovecorev1alpha1.PodCliqueSet) string {
	// Get from first clique's pod spec
	if len(pcs.Spec.Template.Cliques) > 0 {
		return pcs.Spec.Template.Cliques[0].Spec.PodSpec.PriorityClassName
	}
	return ""
}

// getReuseReservationRef gets reuse reservation reference for gang
func getReuseReservationRef(gangName string, replicaIndex int) *NamespacedName {
	// TODO: Implement logic to determine if we should reuse a previous gang's reservation
	// This would be used during rolling updates
	return nil
}

