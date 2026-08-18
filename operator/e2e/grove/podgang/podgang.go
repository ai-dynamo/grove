//go:build e2e

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

// Package podgang provides scheduler-agnostic lookup helpers that resolve PodGangs
// for a PodCliqueSet replica via its PodGangMap (the desired-state source of truth).
//
// Tests should not look up PodGangs by legacy <pcs>-<replica> or <pcsg-fqn>-<index>
// names: those name shapes only apply to PCS resources still on the legacy naming
// scheme (preserved for migration), not to PodGangs created under the unified naming
// convention.
package podgang

import (
	"context"
	"fmt"
	"slices"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ListForPCSReplica returns all PodGangs for a PodCliqueSet replica index from the PodGangMap entries.
// The PodGangMap is the desired-state source of truth for which PodGangs exist for a replica.
func ListForPCSReplica(ctx context.Context, cl client.Client, namespace, pcsName string, pcsReplicaIndex int) ([]groveschedulerv1alpha1.PodGang, error) {
	pgm, err := getPodGangMapForPCSReplica(ctx, cl, namespace, pcsName, pcsReplicaIndex)
	if err != nil {
		return nil, err
	}
	podGangs := make([]groveschedulerv1alpha1.PodGang, 0, len(pgm.Spec.Entries))
	for _, entry := range pgm.Spec.Entries {
		pg, err := getPodGang(ctx, cl, namespace, entry.Name)
		if err != nil {
			return nil, fmt.Errorf("PodGang %q referenced by PodGangMap %s/%s not found: %w", entry.Name, namespace, pgm.Name, err)
		}
		podGangs = append(podGangs, *pg)
	}
	return podGangs, nil
}

// ForPCSGReplica returns the PodGang whose PodGangMap entry holds the given PCSG replica
// index for the named PCSG config. A PCSG replica index belongs to exactly one PodGang.
func ForPCSGReplica(ctx context.Context, cl client.Client, namespace, pcsName string, pcsReplicaIndex int, pcsgConfigName string, pcsgReplicaIndex int32) (*groveschedulerv1alpha1.PodGang, error) {
	pgm, err := getPodGangMapForPCSReplica(ctx, cl, namespace, pcsName, pcsReplicaIndex)
	if err != nil {
		return nil, err
	}
	for _, entry := range pgm.Spec.Entries {
		indices, ok := entry.PCSGReplicaIndices[pcsgConfigName]
		if !ok {
			continue
		}
		if slices.Contains(indices, pcsgReplicaIndex) {
			return getPodGang(ctx, cl, namespace, entry.Name)
		}
	}
	return nil, fmt.Errorf("no PodGang in PodGangMap %s/%s contains PCSG %q replica index %d",
		namespace, pgm.Name, pcsgConfigName, pcsgReplicaIndex)
}

// ListForStandalonePCLQ returns all PodGangs whose PodGangMap entries hold a non-zero
// pod count for the given standalone PodClique name. A standalone PCLQ's pods can be
// distributed across multiple PodGangs (MPGs and a Tail-PG) during coherent updates.
func ListForStandalonePCLQ(ctx context.Context, cl client.Client, namespace, pcsName string, pcsReplicaIndex int, pclqName string) ([]groveschedulerv1alpha1.PodGang, error) {
	pgm, err := getPodGangMapForPCSReplica(ctx, cl, namespace, pcsName, pcsReplicaIndex)
	if err != nil {
		return nil, err
	}
	var podGangs []groveschedulerv1alpha1.PodGang
	for _, entry := range pgm.Spec.Entries {
		count, ok := entry.PodCliques[pclqName]
		if !ok || count == 0 {
			continue
		}
		pg, err := getPodGang(ctx, cl, namespace, entry.Name)
		if err != nil {
			return nil, err
		}
		podGangs = append(podGangs, *pg)
	}
	if len(podGangs) == 0 {
		return nil, fmt.Errorf("no PodGang in PodGangMap %s/%s contains standalone PodClique %q",
			namespace, pgm.Name, pclqName)
	}
	return podGangs, nil
}

func getPodGangMapForPCSReplica(ctx context.Context, cl client.Client, namespace, pcsName string, pcsReplica int) (*grovecorev1alpha1.PodGangMap, error) {
	pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: pcsName, Replica: pcsReplica})
	pgm := &grovecorev1alpha1.PodGangMap{}
	if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: pgmName}, pgm); err != nil {
		return nil, fmt.Errorf("failed to get PodGangMap %s/%s: %w", namespace, pgmName, err)
	}
	return pgm, nil
}

func getPodGang(ctx context.Context, cl client.Client, namespace, name string) (*groveschedulerv1alpha1.PodGang, error) {
	pg := &groveschedulerv1alpha1.PodGang{}
	if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, pg); err != nil {
		return nil, fmt.Errorf("failed to get PodGang %s/%s: %w", namespace, name, err)
	}
	return pg, nil
}
