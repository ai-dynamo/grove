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

package podclique

import (
	"context"
	"testing"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/expect"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestTriggerDeletionFlow verifies the cascade-delete flow: the controller
// clears in-memory expectations and removes the finalizer, leaving the actual
// child cleanup to the Kubernetes garbage collector via owner references.
func TestTriggerDeletionFlow(t *testing.T) {
	tests := []struct {
		name              string
		pclq              *grovecorev1alpha1.PodClique
		expectFinalizer   bool
		expectRequeue     bool
		expectErrors      bool
		seedExpectationOf string
	}{
		{
			name: "removes_finalizer_and_completes",
			pclq: &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-pclq",
					Namespace:  "default",
					Finalizers: []string{constants.FinalizerPodClique},
				},
			},
			seedExpectationOf: "default/test-pclq",
			expectFinalizer:   false,
			expectRequeue:     false,
			expectErrors:      false,
		},
		{
			name: "no_finalizer_is_a_noop",
			pclq: &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pclq",
					Namespace: "default",
				},
			},
			expectFinalizer: false,
			expectRequeue:   false,
			expectErrors:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tc.pclq).
				Build()

			expectationsStore := expect.NewExpectationsStore()
			if tc.seedExpectationOf != "" {
				require.NoError(t, expectationsStore.ExpectCreations(logr.Discard(), tc.seedExpectationOf, "uid-1"))
			}

			r := &Reconciler{
				client:            fakeClient,
				expectationsStore: expectationsStore,
			}

			result := r.triggerDeletionFlow(context.Background(), logr.Discard(), tc.pclq)

			assert.Equal(t, tc.expectRequeue, result.NeedsRequeue())
			assert.Equal(t, tc.expectErrors, result.HasErrors())

			if tc.seedExpectationOf != "" {
				_, exists, err := expectationsStore.GetExpectations(tc.seedExpectationOf)
				require.NoError(t, err)
				assert.False(t, exists, "expectations entry should have been cleared")
			}

			fetched := &grovecorev1alpha1.PodClique{}
			err := fakeClient.Get(context.Background(), types.NamespacedName{Name: tc.pclq.Name, Namespace: tc.pclq.Namespace}, fetched)
			require.NoError(t, err)
			if tc.expectFinalizer {
				assert.Contains(t, fetched.Finalizers, constants.FinalizerPodClique)
			} else {
				assert.NotContains(t, fetched.Finalizers, constants.FinalizerPodClique)
			}
		})
	}
}
