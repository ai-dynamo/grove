// /*
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
// */

package kueue

import (
	"context"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	kueuev1beta2 "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

const (
	// maxPodSetsPerTopologyGroup is how many PodSets of one podSetGroupName Kueue TAS places together: it reads only
	// the first two as leader and workers (findLeaderAndWorkers) and ignores the rest (ai-dynamo/grove#867).
	maxPodSetsPerTopologyGroup = 2
	// topologyGroupLeaderPodCount is the Pod count Kueue TAS assumes for the leader of a two-PodSet group: it places
	// a single leader Pod whatever the PodSet's count (ai-dynamo/grove#867).
	topologyGroupLeaderPodCount = 1
)

// topologyGroup is a set of PodSets that a PodGang asks to be packed into one topology domain, together with that
// domain. It is either a TopologyConstraintGroupConfig (one PodCliqueScalingGroup replica, such as a leader and its
// workers) or the PodGroups that only the PodGang-wide constraint covers.
type topologyGroup struct {
	name      string
	required  string
	preferred string
	members   []*kueuev1beta2.PodSet
}

// applyTopologyGroups sets podSetGroupName on the members of each topology group of podGang that Kueue TAS can place
// together as asked, so a group such as a tensor-parallel leader and its workers lands in one domain. Any other group
// keeps per-PodSet placement. podSets are the Workload's PodSets, one per PodGroup of podGang.
func applyTopologyGroups(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang, podSets []kueuev1beta2.PodSet) {
	for _, group := range resolveTopologyGroups(podGang, podSets) {
		if reason := group.unsupportedReason(); reason != "" {
			log.FromContext(ctx).V(1).Info("Placing the PodSets of a topology group independently because Kueue cannot place them together as asked",
				"podGang", client.ObjectKeyFromObject(podGang), "topologyGroup", group.name, "reason", reason)
			continue
		}
		for _, member := range group.members {
			member.TopologyRequest.PodSetGroupName = ptr.To(group.name)
		}
	}
}

// resolveTopologyGroups returns the topology groups of podGang, with members taken from podSets: one per
// TopologyConstraintGroupConfig with a pack constraint, then one for the remaining PodGroups when the PodGang requires
// a domain for all of its Pods.
func resolveTopologyGroups(podGang *groveschedulerv1alpha1.PodGang, podSets []kueuev1beta2.PodSet) []topologyGroup {
	podSetByName := make(map[kueuev1beta2.PodSetReference]*kueuev1beta2.PodSet, len(podSets))
	for i := range podSets {
		podSetByName[podSets[i].Name] = &podSets[i]
	}
	memberPodSets := func(podGroupNames []string) []*kueuev1beta2.PodSet {
		members := make([]*kueuev1beta2.PodSet, 0, len(podGroupNames))
		for _, name := range podGroupNames {
			if podSet := podSetByName[kueuev1beta2.NewPodSetReference(name)]; podSet != nil {
				members = append(members, podSet)
			}
		}
		return members
	}

	var groups []topologyGroup
	inGroupConfig := sets.New[string]()
	for _, config := range podGang.Spec.TopologyConstraintGroupConfigs {
		if config.TopologyConstraint == nil || config.TopologyConstraint.PackConstraint == nil {
			continue
		}
		groups = append(groups, newTopologyGroup(config.Name, config.TopologyConstraint.PackConstraint, memberPodSets(config.PodGroupNames)))
		inGroupConfig.Insert(config.PodGroupNames...)
	}

	if constraint := podGang.Spec.TopologyConstraint; constraint != nil && constraint.PackConstraint != nil &&
		ptr.Deref(constraint.PackConstraint.Required, "") != "" {
		var rest []string
		for _, podGroup := range podGang.Spec.PodGroups {
			if !inGroupConfig.Has(podGroup.Name) {
				rest = append(rest, podGroup.Name)
			}
		}
		if len(rest) > 0 {
			groups = append(groups, newTopologyGroup(podGang.Name, constraint.PackConstraint, memberPodSets(rest)))
		}
	}
	return groups
}

func newTopologyGroup(name string, constraint *groveschedulerv1alpha1.TopologyPackConstraint, members []*kueuev1beta2.PodSet) topologyGroup {
	return topologyGroup{
		name:      name,
		required:  ptr.Deref(constraint.Required, ""),
		preferred: ptr.Deref(constraint.Preferred, ""),
		members:   members,
	}
}

// unsupportedReason returns why Kueue TAS cannot place the group's members together exactly as the group asks, or ""
// if it can. Kueue supports only part of this (ai-dynamo/grove#867):
//   - the group must ask for a required or preferred domain, since Kueue rejects podSetGroupName otherwise;
//   - it may have at most maxPodSetsPerTopologyGroup members, since Kueue silently ignores the rest;
//   - a two-member group needs a member with topologyGroupLeaderPodCount Pods, since Kueue places only that many Pods
//     of the leader, the member with the smaller count;
//   - every member's topology request must be the group's own, since Kueue places the whole group using the workers'
//     request. A narrower member constraint, such as workers packed onto one host within a rack-packed group, would
//     force the leader onto the workers' host as well and could leave a Workload that fits the rack pending.
func (g topologyGroup) unsupportedReason() string {
	if g.required == "" && g.preferred == "" {
		return "the group requests no topology domain"
	}
	if len(g.members) > maxPodSetsPerTopologyGroup {
		return "Kueue places at most two PodSets of a group together"
	}
	if len(g.members) == maxPodSetsPerTopologyGroup && !g.hasSinglePodLeader() {
		return "Kueue places only one Pod of the leader PodSet"
	}
	for _, member := range g.members {
		if !g.matchesRequest(member.TopologyRequest) {
			return "a member's topology constraint differs from the group's"
		}
	}
	return ""
}

func (g topologyGroup) hasSinglePodLeader() bool {
	for _, member := range g.members {
		if member.Count == topologyGroupLeaderPodCount {
			return true
		}
	}
	return false
}

func (g topologyGroup) matchesRequest(request *kueuev1beta2.PodSetTopologyRequest) bool {
	return request != nil &&
		ptr.Deref(request.Required, "") == g.required &&
		ptr.Deref(request.Preferred, "") == g.preferred
}
