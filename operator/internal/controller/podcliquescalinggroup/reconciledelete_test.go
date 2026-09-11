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

package podcliquescalinggroup

import (
	"context"
	"testing"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	pcsgexpectations "github.com/ai-dynamo/grove/operator/internal/controller/podcliquescalinggroup/expectations"
	"github.com/ai-dynamo/grove/operator/internal/expect"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestTriggerDeletionFlow verifies the PodCliqueScalingGroup cascade-delete flow: the controller
// clears its in-memory delete expectations and removes the finalizer, so the store does not leak an
// entry per PodCliqueScalingGroup lifecycle while the Kubernetes garbage collector cascades child
// cleanup via owner references.
func TestTriggerDeletionFlow(t *testing.T) {
	tests := []struct {
		name            string
		pcsg            *grovecorev1alpha1.PodCliqueScalingGroup
		seedExpectation bool
	}{
		{
			name: "clears_expectations_and_removes_finalizer",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-pcsg",
					Namespace:  "default",
					Finalizers: []string{constants.FinalizerPodCliqueScalingGroup},
				},
			},
			seedExpectation: true,
		},
		{
			name: "no_finalizer_is_a_noop",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcsg",
					Namespace: "default",
				},
			},
			seedExpectation: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tc.pcsg).
				Build()

			expectationStore := expect.NewExpectationsStore()
			key, err := pcsgexpectations.PCSGScopedExpectationsStoreKey(tc.pcsg.ObjectMeta)
			require.NoError(t, err)
			if tc.seedExpectation {
				require.NoError(t, expectationStore.ExpectDeletions(logr.Discard(), key, types.UID("uid-1")))
			}

			r := &Reconciler{
				client:           fakeClient,
				expectationStore: expectationStore,
			}

			result := r.triggerDeletionFlow(context.Background(), logr.Discard(), tc.pcsg)
			assert.False(t, result.HasErrors())

			// The expectations entry must not leak after deletion.
			_, exists, err := expectationStore.GetExpectations(key)
			require.NoError(t, err)
			assert.False(t, exists, "expectations entry should have been cleared, no leak")

			fetched := &grovecorev1alpha1.PodCliqueScalingGroup{}
			require.NoError(t, fakeClient.Get(context.Background(), types.NamespacedName{Name: tc.pcsg.Name, Namespace: tc.pcsg.Namespace}, fetched))
			assert.NotContains(t, fetched.Finalizers, constants.FinalizerPodCliqueScalingGroup)
		})
	}
}
