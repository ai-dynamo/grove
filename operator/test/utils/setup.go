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

package utils

import (
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// ============================================================================
// Common Test Setup Functions
// ============================================================================

// SetupFakeClient creates a fake Kubernetes client with Grove CRDs and status subresources.
func SetupFakeClient(objects ...client.Object) client.WithWatch {
	scheme := runtime.NewScheme()
	utilruntime.Must(grovecorev1alpha1.AddToScheme(scheme))
	utilruntime.Must(v1.AddToScheme(scheme))

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&grovecorev1alpha1.PodCliqueSet{}).
		WithStatusSubresource(&grovecorev1alpha1.PodCliqueScalingGroup{}).
		WithStatusSubresource(&grovecorev1alpha1.PodClique{}).
		WithStatusSubresource(&v1.Pod{}).
		WithIndex(&v1.Pod{}, ".metadata.controller.uid", func(obj client.Object) []string {
			controllerRef := metav1.GetControllerOfNoCopy(obj)
			if controllerRef == nil {
				return nil
			}
			return []string{string(controllerRef.UID)}
		}).
		WithObjects(objects...).
		Build()
}
