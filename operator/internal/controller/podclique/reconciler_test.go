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

package podclique

import (
	"context"
	"errors"
	"testing"
	"time"

	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestGetStatusResyncInterval(t *testing.T) {
	const (
		pcsName   = "pcs"
		namespace = "default"
	)
	// expected values are literals, not constants.PodCliqueStatusResyncInterval, so the test does not
	// pass by construction. A change to the resync interval must consciously update these.
	tests := []struct {
		name            string
		breachTolerance *time.Duration
		getPCSFails     bool
		expected        time.Duration
	}{
		{
			name:            "breachTolerance shorter than the resync interval bounds the interval",
			breachTolerance: ptr.To(2 * time.Minute),
			expected:        2 * time.Minute,
		},
		{
			name:            "breachTolerance longer than the resync interval keeps the resync interval",
			breachTolerance: ptr.To(4 * time.Hour),
			expected:        3 * time.Minute,
		},
		{
			name:            "no breachTolerance falls back to the resync interval",
			breachTolerance: nil,
			expected:        3 * time.Minute,
		},
		{
			name:        "PodCliqueSet fetch failure falls back to the resync interval",
			getPCSFails: true,
			expected:    3 * time.Minute,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcs := testutils.NewPodCliqueSetBuilder(pcsName, namespace, "uid").Build()
			pclq := testutils.NewPodCliqueBuilder(pcsName, "uid", "clq", namespace, 0).Build()
			if tt.breachTolerance != nil {
				pclq.Spec.Termination.Resource.Availability.BreachTolerance = &metav1.Duration{Duration: *tt.breachTolerance}
			}

			clientBuilder := testutils.NewTestClientBuilder().WithObjects(pcs, pclq)
			if tt.getPCSFails {
				clientBuilder.RecordErrorForObjects(testutils.ClientMethodGet,
					apierrors.NewInternalError(errors.New("boom")),
					client.ObjectKey{Name: pcsName, Namespace: namespace})
			}
			r := &Reconciler{client: clientBuilder.Build()}

			actual := r.getStatusResyncInterval(context.Background(), pclq)
			assert.Equal(t, tt.expected, actual)
		})
	}
}
