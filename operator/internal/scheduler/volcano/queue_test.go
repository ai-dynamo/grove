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

package volcano

import (
	"context"
	"testing"

	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	volcanov1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

func TestEffectiveQueueFromAnnotations(t *testing.T) {
	assert.Equal(t, DefaultQueue, EffectiveQueueFromAnnotations(nil))
	assert.Equal(t, DefaultQueue, EffectiveQueueFromAnnotations(map[string]string{}))
	assert.Equal(t, DefaultQueue, EffectiveQueueFromAnnotations(map[string]string{QueueAnnotationKey: ""}))
	assert.Equal(t, DefaultQueue, EffectiveQueueFromAnnotations(map[string]string{QueueAnnotationKey: "   "}))
	assert.Equal(t, "gpu-training", EffectiveQueueFromAnnotations(map[string]string{QueueAnnotationKey: "gpu-training"}))
}

func TestResolvePodCliqueQueue(t *testing.T) {
	tests := []struct {
		name              string
		globalAnnotations map[string]string
		cliqueAnnotations map[string]string
		expectedQueue     string
		expectErr         error
	}{
		{
			name:              "both unset defaults to default",
			globalAnnotations: nil,
			cliqueAnnotations: nil,
			expectedQueue:     DefaultQueue,
		},
		{
			name:              "global only",
			globalAnnotations: map[string]string{QueueAnnotationKey: "gpu-training"},
			cliqueAnnotations: nil,
			expectedQueue:     "gpu-training",
		},
		{
			name:              "clique only",
			globalAnnotations: nil,
			cliqueAnnotations: map[string]string{QueueAnnotationKey: "gpu-training"},
			expectedQueue:     "gpu-training",
		},
		{
			name:              "both same",
			globalAnnotations: map[string]string{QueueAnnotationKey: "gpu-training"},
			cliqueAnnotations: map[string]string{QueueAnnotationKey: "gpu-training"},
			expectedQueue:     "gpu-training",
		},
		{
			name:              "conflict",
			globalAnnotations: map[string]string{QueueAnnotationKey: "gpu-training"},
			cliqueAnnotations: map[string]string{QueueAnnotationKey: "high-priority"},
			expectErr:         ErrConflictingQueueAnnotations,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queue, err := ResolvePodCliqueQueue(tt.globalAnnotations, tt.cliqueAnnotations)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectedQueue, queue)
		})
	}
}

func TestValidateQueueExistsAndIsOpen(t *testing.T) {
	makeQueue := func(name string, state volcanov1beta1.QueueState) client.Object {
		return &volcanov1beta1.Queue{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Status:     volcanov1beta1.QueueStatus{State: state},
		}
	}

	t.Run("existing open queue", func(t *testing.T) {
		cl := testutils.CreateDefaultFakeClient([]client.Object{makeQueue("gpu-training", volcanov1beta1.QueueStateOpen)})
		require.NoError(t, ValidateQueueExistsAndIsOpen(context.Background(), cl, "gpu-training"))
	})

	t.Run("missing queue", func(t *testing.T) {
		cl := testutils.CreateDefaultFakeClient(nil)
		err := ValidateQueueExistsAndIsOpen(context.Background(), cl, "gpu-training")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not exist")
	})

	t.Run("queue not open", func(t *testing.T) {
		cl := testutils.CreateDefaultFakeClient([]client.Object{makeQueue("gpu-training", "Closed")})
		err := ValidateQueueExistsAndIsOpen(context.Background(), cl, "gpu-training")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not Open")
	})
}
