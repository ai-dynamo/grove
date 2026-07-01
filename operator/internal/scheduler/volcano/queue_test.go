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

	schedulertest "github.com/ai-dynamo/grove/operator/test/utils/scheduler"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	volcanov1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

func TestEffectiveQueueFromAnnotations(t *testing.T) {
	assert.Equal(t, DefaultQueue, effectiveQueueFromAnnotations(nil))
	assert.Equal(t, DefaultQueue, effectiveQueueFromAnnotations(map[string]string{}))
	assert.Equal(t, DefaultQueue, effectiveQueueFromAnnotations(map[string]string{QueueAnnotationKey: ""}))
	assert.Equal(t, DefaultQueue, effectiveQueueFromAnnotations(map[string]string{QueueAnnotationKey: "   "}))
	assert.Equal(t, "gpu-training", effectiveQueueFromAnnotations(map[string]string{QueueAnnotationKey: "gpu-training"}))
}

func TestResolvePodCliqueQueue(t *testing.T) {
	tests := []struct {
		name          string
		globalQueue   string
		cliqueQueue   string
		expectedQueue string
		expectErr     error
	}{
		{
			name:          "both unset defaults to default",
			expectedQueue: DefaultQueue,
		},
		{
			name:          "global only",
			globalQueue:   "gpu-training",
			expectedQueue: "gpu-training",
		},
		{
			name:          "clique only",
			cliqueQueue:   "gpu-training",
			expectedQueue: "gpu-training",
		},
		{
			name:          "both same",
			globalQueue:   "gpu-training",
			cliqueQueue:   "gpu-training",
			expectedQueue: "gpu-training",
		},
		{
			name:        "conflict",
			globalQueue: "gpu-training",
			cliqueQueue: "high-priority",
			expectErr:   errConflictingQueueAnnotations,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queue, err := resolvePodCliqueQueue(tt.globalQueue, tt.cliqueQueue)
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
		cl := schedulertest.NewVolcanoClient(t, makeQueue("gpu-training", volcanov1beta1.QueueStateOpen))
		require.NoError(t, validateQueueExistsAndIsOpen(context.Background(), cl, "gpu-training"))
	})

	t.Run("missing queue", func(t *testing.T) {
		cl := schedulertest.NewVolcanoClient(t)
		err := validateQueueExistsAndIsOpen(context.Background(), cl, "gpu-training")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not exist")
	})

	t.Run("queue not open", func(t *testing.T) {
		cl := schedulertest.NewVolcanoClient(t, makeQueue("gpu-training", "Closed"))
		err := validateQueueExistsAndIsOpen(context.Background(), cl, "gpu-training")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not Open")
	})
}
