package utils

import (
	"context"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetClusterTopologyLevels retrieves the TopologyLevels from the specified ClusterTopology resource.
func GetClusterTopologyLevels(ctx context.Context, cl client.Client, name string) ([]grovecorev1alpha1.TopologyLevel, error) {
	clusterTopology := &grovecorev1alpha1.ClusterTopology{}
	if err := cl.Get(ctx, client.ObjectKey{Name: name}, clusterTopology); err != nil {
		return nil, err
	}
	return clusterTopology.Spec.Levels, nil
}
