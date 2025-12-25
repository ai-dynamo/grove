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

package topology

import (
	"context"
	"testing"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	kaitopologyv1alpha1 "github.com/NVIDIA/KAI-scheduler/pkg/apis/kai/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestConvertTopologyLevels(t *testing.T) {
	configLevels := []configv1alpha1.TopologyLevel{
		{Domain: "region", Key: "topology.kubernetes.io/region"},
		{Domain: "zone", Key: "topology.kubernetes.io/zone"},
	}

	coreLevels := convertTopologyLevels(configLevels)

	require.Len(t, coreLevels, 2)
	assert.Equal(t, corev1alpha1.TopologyDomain("region"), coreLevels[0].Domain)
	assert.Equal(t, "topology.kubernetes.io/region", coreLevels[0].Key)
	assert.Equal(t, corev1alpha1.TopologyDomain("zone"), coreLevels[1].Domain)
	assert.Equal(t, "topology.kubernetes.io/zone", coreLevels[1].Key)
}

func TestConvertClusterTopologyToKai(t *testing.T) {
	clusterTopology := &corev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: "grove-topology",
		},
		Spec: corev1alpha1.ClusterTopologySpec{
			Levels: []corev1alpha1.TopologyLevel{
				{Domain: "zone", Key: "topology.kubernetes.io/zone"},
				{Domain: "region", Key: "topology.kubernetes.io/region"},
			},
		},
	}

	kaiTopology := convertClusterTopologyToKai(clusterTopology)

	assert.Equal(t, "grove-topology", kaiTopology.Name)
	require.Len(t, kaiTopology.Spec.Levels, 2)
	assert.Equal(t, "topology.kubernetes.io/region", kaiTopology.Spec.Levels[0].NodeLabel)
	assert.Equal(t, "topology.kubernetes.io/zone", kaiTopology.Spec.Levels[1].NodeLabel)
}

func TestEnsureTopology(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(builder *fake.ClientBuilder)
		levels      []configv1alpha1.TopologyLevel
		wantErr     bool
		errContains string
		validate    func(*testing.T, client.Client)
	}{
		{
			name:  "create_new",
			setup: nil,
			levels: []configv1alpha1.TopologyLevel{
				{Domain: "zone", Key: "topology.kubernetes.io/zone"},
			},
			wantErr: false,
			validate: func(t *testing.T, c client.Client) {
				ct := &corev1alpha1.ClusterTopology{}
				err := c.Get(context.Background(), types.NamespacedName{Name: "test-topology"}, ct)
				require.NoError(t, err)
				assert.Len(t, ct.Spec.Levels, 1)
				assert.Equal(t, corev1alpha1.TopologyDomain("zone"), ct.Spec.Levels[0].Domain)

				kai := &kaitopologyv1alpha1.Topology{}
				err = c.Get(context.Background(), types.NamespacedName{Name: "test-topology"}, kai)
				require.NoError(t, err)
				assert.Len(t, kai.Spec.Levels, 1)
				assert.Equal(t, "topology.kubernetes.io/zone", kai.Spec.Levels[0].NodeLabel)

				ownerRef := metav1.GetControllerOf(kai)
				require.NotNil(t, ownerRef)
				assert.Equal(t, "test-topology", ownerRef.Name)
			},
		},
		{
			name: "update_cluster_create_kai",
			setup: func(builder *fake.ClientBuilder) {
				existing := &corev1alpha1.ClusterTopology{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-topology",
						UID:  types.UID("cluster-uid"),
					},
					Spec: corev1alpha1.ClusterTopologySpec{
						Levels: []corev1alpha1.TopologyLevel{
							{Domain: "zone", Key: "old-key"},
						},
					},
				}
				builder.WithObjects(existing)
			},
			levels: []configv1alpha1.TopologyLevel{
				{Domain: "zone", Key: "new-key"},
			},
			wantErr: false,
			validate: func(t *testing.T, c client.Client) {
				ct := &corev1alpha1.ClusterTopology{}
				err := c.Get(context.Background(), types.NamespacedName{Name: "test-topology"}, ct)
				require.NoError(t, err)
				assert.Equal(t, "new-key", ct.Spec.Levels[0].Key)

				kai := &kaitopologyv1alpha1.Topology{}
				err = c.Get(context.Background(), types.NamespacedName{Name: "test-topology"}, kai)
				require.NoError(t, err)
				assert.Len(t, kai.Spec.Levels, 1)
				assert.Equal(t, "new-key", kai.Spec.Levels[0].NodeLabel)
			},
		},
		{
			name: "both_exist_unchanged",
			setup: func(builder *fake.ClientBuilder) {
				ct := &corev1alpha1.ClusterTopology{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-topology",
						UID:  types.UID("cluster-uid"),
					},
					Spec: corev1alpha1.ClusterTopologySpec{
						Levels: []corev1alpha1.TopologyLevel{
							{Domain: "region", Key: "topology.kubernetes.io/region"},
						},
					},
				}

				kai := &kaitopologyv1alpha1.Topology{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-topology",
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion:         "core.grove.io/v1alpha1",
								Kind:               "ClusterTopology",
								Name:               "test-topology",
								UID:                ct.UID,
								Controller:         ptr.To(true),
								BlockOwnerDeletion: ptr.To(true),
							},
						},
					},
					Spec: kaitopologyv1alpha1.TopologySpec{
						Levels: []kaitopologyv1alpha1.TopologyLevel{
							{NodeLabel: "topology.kubernetes.io/region"},
						},
					},
				}

				builder.WithObjects(ct, kai)
			},
			levels: []configv1alpha1.TopologyLevel{
				{Domain: "region", Key: "topology.kubernetes.io/region"},
			},
			wantErr: false,
			validate: func(t *testing.T, c client.Client) {
				ct := &corev1alpha1.ClusterTopology{}
				err := c.Get(context.Background(), types.NamespacedName{Name: "test-topology"}, ct)
				require.NoError(t, err)
				assert.Len(t, ct.Spec.Levels, 1)

				kai := &kaitopologyv1alpha1.Topology{}
				err = c.Get(context.Background(), types.NamespacedName{Name: "test-topology"}, kai)
				require.NoError(t, err)
				assert.Len(t, kai.Spec.Levels, 1)
			},
		},
		{
			name: "both_need_changes",
			setup: func(builder *fake.ClientBuilder) {
				ct := &corev1alpha1.ClusterTopology{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-topology",
						UID:  types.UID("cluster-uid"),
					},
					Spec: corev1alpha1.ClusterTopologySpec{
						Levels: []corev1alpha1.TopologyLevel{
							{Domain: "region", Key: "old-region-key"},
						},
					},
				}

				kai := &kaitopologyv1alpha1.Topology{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-topology",
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion:         "core.grove.io/v1alpha1",
								Kind:               "ClusterTopology",
								Name:               "test-topology",
								UID:                ct.UID,
								Controller:         ptr.To(true),
								BlockOwnerDeletion: ptr.To(true),
							},
						},
					},
					Spec: kaitopologyv1alpha1.TopologySpec{
						Levels: []kaitopologyv1alpha1.TopologyLevel{
							{NodeLabel: "old-region-key"},
						},
					},
				}

				builder.WithObjects(ct, kai)
			},
			levels: []configv1alpha1.TopologyLevel{
				{Domain: "region", Key: "new-region-key"},
				{Domain: "zone", Key: "topology.kubernetes.io/zone"},
			},
			wantErr: false,
			validate: func(t *testing.T, c client.Client) {
				ct := &corev1alpha1.ClusterTopology{}
				err := c.Get(context.Background(), types.NamespacedName{Name: "test-topology"}, ct)
				require.NoError(t, err)
				assert.Len(t, ct.Spec.Levels, 2)
				assert.Equal(t, "new-region-key", ct.Spec.Levels[0].Key)

				kai := &kaitopologyv1alpha1.Topology{}
				err = c.Get(context.Background(), types.NamespacedName{Name: "test-topology"}, kai)
				require.NoError(t, err)
				assert.Len(t, kai.Spec.Levels, 2)
			},
		},
		{
			name:    "empty_levels",
			setup:   nil,
			levels:  []configv1alpha1.TopologyLevel{},
			wantErr: false,
			validate: func(t *testing.T, c client.Client) {
				ct := &corev1alpha1.ClusterTopology{}
				err := c.Get(context.Background(), types.NamespacedName{Name: "test-topology"}, ct)
				require.NoError(t, err)
				assert.Len(t, ct.Spec.Levels, 0)

				kai := &kaitopologyv1alpha1.Topology{}
				err = c.Get(context.Background(), types.NamespacedName{Name: "test-topology"}, kai)
				require.NoError(t, err)
				assert.Len(t, kai.Spec.Levels, 0)
			},
		},
		{
			name: "kai_wrong_owner",
			setup: func(builder *fake.ClientBuilder) {
				ct := &corev1alpha1.ClusterTopology{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-topology",
						UID:  types.UID("cluster-uid"),
					},
					Spec: corev1alpha1.ClusterTopologySpec{
						Levels: []corev1alpha1.TopologyLevel{
							{Domain: "region", Key: "topology.kubernetes.io/region"},
						},
					},
				}

				kai := &kaitopologyv1alpha1.Topology{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-topology",
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion:         "core.grove.io/v1alpha1",
								Kind:               "ClusterTopology",
								Name:               "different-owner",
								UID:                types.UID("different-uid"),
								Controller:         ptr.To(true),
								BlockOwnerDeletion: ptr.To(true),
							},
						},
					},
					Spec: kaitopologyv1alpha1.TopologySpec{
						Levels: []kaitopologyv1alpha1.TopologyLevel{
							{NodeLabel: "topology.kubernetes.io/region"},
						},
					},
				}

				builder.WithObjects(ct, kai)
			},
			levels: []configv1alpha1.TopologyLevel{
				{Domain: "region", Key: "topology.kubernetes.io/region"},
			},
			wantErr:     true,
			errContains: "owned by a different controller",
		},
		{
			name: "kai_no_owner",
			setup: func(builder *fake.ClientBuilder) {
				ct := &corev1alpha1.ClusterTopology{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-topology",
						UID:  types.UID("cluster-uid"),
					},
					Spec: corev1alpha1.ClusterTopologySpec{
						Levels: []corev1alpha1.TopologyLevel{
							{Domain: "region", Key: "topology.kubernetes.io/region"},
						},
					},
				}

				kai := &kaitopologyv1alpha1.Topology{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-topology",
					},
					Spec: kaitopologyv1alpha1.TopologySpec{
						Levels: []kaitopologyv1alpha1.TopologyLevel{
							{NodeLabel: "topology.kubernetes.io/region"},
						},
					},
				}

				builder.WithObjects(ct, kai)
			},
			levels: []configv1alpha1.TopologyLevel{
				{Domain: "region", Key: "topology.kubernetes.io/region"},
			},
			wantErr:     true,
			errContains: "owned by a different controller",
		},
		{
			name: "levels_change_recreate_kai",
			setup: func(builder *fake.ClientBuilder) {
				ct := &corev1alpha1.ClusterTopology{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-topology",
						UID:  types.UID("cluster-uid"),
					},
					Spec: corev1alpha1.ClusterTopologySpec{
						Levels: []corev1alpha1.TopologyLevel{
							{Domain: "region", Key: "topology.kubernetes.io/region"},
						},
					},
				}

				kai := &kaitopologyv1alpha1.Topology{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-topology",
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion:         "core.grove.io/v1alpha1",
								Kind:               "ClusterTopology",
								Name:               "test-topology",
								UID:                ct.UID,
								Controller:         ptr.To(true),
								BlockOwnerDeletion: ptr.To(true),
							},
						},
					},
					Spec: kaitopologyv1alpha1.TopologySpec{
						Levels: []kaitopologyv1alpha1.TopologyLevel{
							{NodeLabel: "old-label"},
						},
					},
				}

				builder.WithObjects(ct, kai)
			},
			levels: []configv1alpha1.TopologyLevel{
				{Domain: "region", Key: "topology.kubernetes.io/region"},
				{Domain: "zone", Key: "topology.kubernetes.io/zone"},
			},
			wantErr: false,
			validate: func(t *testing.T, c client.Client) {
				ct := &corev1alpha1.ClusterTopology{}
				err := c.Get(context.Background(), types.NamespacedName{Name: "test-topology"}, ct)
				require.NoError(t, err)
				assert.Len(t, ct.Spec.Levels, 2)

				kai := &kaitopologyv1alpha1.Topology{}
				err = c.Get(context.Background(), types.NamespacedName{Name: "test-topology"}, kai)
				require.NoError(t, err)
				assert.Len(t, kai.Spec.Levels, 2)
				assert.Equal(t, "topology.kubernetes.io/region", kai.Spec.Levels[0].NodeLabel)
				assert.Equal(t, "topology.kubernetes.io/zone", kai.Spec.Levels[1].NodeLabel)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = corev1alpha1.AddToScheme(scheme)
			_ = kaitopologyv1alpha1.AddToScheme(scheme)

			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.setup != nil {
				tt.setup(builder)
			}
			c := builder.Build()

			err := EnsureTopology(context.Background(), c, "test-topology", tt.levels)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
				if tt.validate != nil {
					tt.validate(t, c)
				}
			}
		})
	}
}

func TestEnsureDeleteClusterTopology(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(builder *fake.ClientBuilder)
		wantErr     bool
		errContains string
		validate    func(*testing.T, client.Client)
	}{
		{
			name: "successful_delete",
			setup: func(builder *fake.ClientBuilder) {
				ct := &corev1alpha1.ClusterTopology{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-topology",
					},
					Spec: corev1alpha1.ClusterTopologySpec{
						Levels: []corev1alpha1.TopologyLevel{
							{Domain: "region", Key: "topology.kubernetes.io/region"},
						},
					},
				}
				builder.WithObjects(ct)
			},
			wantErr: false,
			validate: func(t *testing.T, c client.Client) {
				ct := &corev1alpha1.ClusterTopology{}
				err := c.Get(context.Background(), types.NamespacedName{Name: "test-topology"}, ct)
				require.Error(t, err)
				assert.True(t, apierrors.IsNotFound(err))
			},
		},
		{
			name:    "already_deleted",
			setup:   nil,
			wantErr: false,
			validate: func(t *testing.T, c client.Client) {
				ct := &corev1alpha1.ClusterTopology{}
				err := c.Get(context.Background(), types.NamespacedName{Name: "test-topology"}, ct)
				require.Error(t, err)
				assert.True(t, apierrors.IsNotFound(err))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = corev1alpha1.AddToScheme(scheme)

			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.setup != nil {
				tt.setup(builder)
			}
			c := builder.Build()

			err := EnsureDeleteClusterTopology(context.Background(), c, "test-topology")

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
				if tt.validate != nil {
					tt.validate(t, c)
				}
			}
		})
	}
}
