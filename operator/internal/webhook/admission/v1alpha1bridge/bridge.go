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

// Package v1alpha1bridge converts the v1beta1 objects the admission webhooks receive into the v1alpha1 types
// the defaulting and validation logic is written against. The admission webhooks match every served version
// with matchPolicy Equivalent, so the API server sends them v1beta1 objects for v1alpha1 requests too.
package v1alpha1bridge

import (
	"fmt"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	"k8s.io/apimachinery/pkg/runtime"
)

// PodCliqueSet returns obj as a v1alpha1 PodCliqueSet. A v1beta1 PodCliqueSet is converted. The result shares
// memory with obj, so a change to it must be written back with SetPodCliqueSet.
func PodCliqueSet(obj runtime.Object) (*grovecorev1alpha1.PodCliqueSet, error) {
	switch pcs := obj.(type) {
	case *grovecorev1alpha1.PodCliqueSet:
		return pcs, nil
	case *grovecorev1beta1.PodCliqueSet:
		out := &grovecorev1alpha1.PodCliqueSet{}
		if err := grovecorev1alpha1.Convert_v1beta1_PodCliqueSet_To_v1alpha1_PodCliqueSet(pcs, out, nil); err != nil {
			return nil, err
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected a PodCliqueSet object but got %T", obj)
	}
}

// SetPodCliqueSet writes pcs, which was returned by PodCliqueSet(dst), back into dst.
func SetPodCliqueSet(pcs *grovecorev1alpha1.PodCliqueSet, dst runtime.Object) error {
	switch out := dst.(type) {
	case *grovecorev1alpha1.PodCliqueSet:
		if out != pcs {
			pcs.DeepCopyInto(out)
		}
		return nil
	case *grovecorev1beta1.PodCliqueSet:
		return grovecorev1alpha1.Convert_v1alpha1_PodCliqueSet_To_v1beta1_PodCliqueSet(pcs, out, nil)
	default:
		return fmt.Errorf("expected a PodCliqueSet object but got %T", dst)
	}
}

// ClusterTopologyBinding returns obj as a v1alpha1 ClusterTopologyBinding. A v1beta1 ClusterTopologyBinding
// is converted.
func ClusterTopologyBinding(obj runtime.Object) (*grovecorev1alpha1.ClusterTopologyBinding, error) {
	switch ctb := obj.(type) {
	case *grovecorev1alpha1.ClusterTopologyBinding:
		return ctb, nil
	case *grovecorev1beta1.ClusterTopologyBinding:
		out := &grovecorev1alpha1.ClusterTopologyBinding{}
		if err := grovecorev1alpha1.Convert_v1beta1_ClusterTopologyBinding_To_v1alpha1_ClusterTopologyBinding(ctb, out, nil); err != nil {
			return nil, err
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected a ClusterTopologyBinding object but got %T", obj)
	}
}
