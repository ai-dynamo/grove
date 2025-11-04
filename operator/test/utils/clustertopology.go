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
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterTopologyBuilder provides a builder for ClusterTopology test objects.
type ClusterTopologyBuilder struct {
	name              string
	levels            []grovecorev1alpha1.TopologyLevel
	finalizers        []string
	deletionTimestamp *metav1.Time
}

// NewClusterTopologyBuilder creates a new builder for ClusterTopology.
func NewClusterTopologyBuilder(name string) *ClusterTopologyBuilder {
	return &ClusterTopologyBuilder{
		name:       name,
		levels:     []grovecorev1alpha1.TopologyLevel{},
		finalizers: []string{},
	}
}

// WithLevels adds topology levels.
func (b *ClusterTopologyBuilder) WithLevels(levels []grovecorev1alpha1.TopologyLevel) *ClusterTopologyBuilder {
	b.levels = levels
	return b
}

// WithFinalizer adds a finalizer.
func (b *ClusterTopologyBuilder) WithFinalizer(finalizer string) *ClusterTopologyBuilder {
	b.finalizers = append(b.finalizers, finalizer)
	return b
}

// WithDeletionTimestamp sets deletion timestamp.
func (b *ClusterTopologyBuilder) WithDeletionTimestamp(timestamp metav1.Time) *ClusterTopologyBuilder {
	b.deletionTimestamp = &timestamp
	return b
}

// Build constructs the ClusterTopology.
func (b *ClusterTopologyBuilder) Build() *grovecorev1alpha1.ClusterTopology {
	ct := &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name:       b.name,
			Finalizers: b.finalizers,
		},
		Spec: grovecorev1alpha1.ClusterTopologySpec{
			Levels: b.levels,
		},
	}

	if b.deletionTimestamp != nil {
		ct.ObjectMeta.DeletionTimestamp = b.deletionTimestamp
	}

	return ct
}

// NewSimpleClusterTopology creates a basic ClusterTopology for testing.
func NewSimpleClusterTopology(name string) *grovecorev1alpha1.ClusterTopology {
	return NewClusterTopologyBuilder(name).
		WithLevels([]grovecorev1alpha1.TopologyLevel{
			{
				Domain: grovecorev1alpha1.TopologyDomainZone,
				Key:    "topology.kubernetes.io/zone",
			},
			{
				Domain: grovecorev1alpha1.TopologyDomainHost,
				Key:    "kubernetes.io/hostname",
			},
		}).
		Build()
}

// NewClusterTopologyWithFinalizer creates ClusterTopology with finalizer.
func NewClusterTopologyWithFinalizer(name string) *grovecorev1alpha1.ClusterTopology {
	return NewClusterTopologyBuilder(name).
		WithLevels([]grovecorev1alpha1.TopologyLevel{
			{
				Domain: grovecorev1alpha1.TopologyDomainZone,
				Key:    "topology.kubernetes.io/zone",
			},
			{
				Domain: grovecorev1alpha1.TopologyDomainHost,
				Key:    "kubernetes.io/hostname",
			},
		}).
		WithFinalizer(constants.FinalizerClusterTopology).
		Build()
}

// NewClusterTopologyMarkedForDeletion creates ClusterTopology with deletion timestamp.
func NewClusterTopologyMarkedForDeletion(name string, hasFinalizer bool) *grovecorev1alpha1.ClusterTopology {
	builder := NewClusterTopologyBuilder(name).
		WithLevels([]grovecorev1alpha1.TopologyLevel{
			{
				Domain: grovecorev1alpha1.TopologyDomainZone,
				Key:    "topology.kubernetes.io/zone",
			},
			{
				Domain: grovecorev1alpha1.TopologyDomainHost,
				Key:    "kubernetes.io/hostname",
			},
		}).
		WithDeletionTimestamp(metav1.Now())

	if hasFinalizer {
		builder = builder.WithFinalizer(constants.FinalizerClusterTopology)
	}

	return builder.Build()
}
