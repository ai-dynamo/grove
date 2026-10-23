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

package pod

import (
	"testing"

	pclqexp "github.com/ai-dynamo/grove/operator/internal/controller/podclique/expectations"
	"github.com/ai-dynamo/grove/operator/internal/expect"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// After an operator restart the expectations store is empty. A Pod that was already terminating must
// still be netted out of the live count, otherwise its replacement is never created.
func TestReconcileLivePodCountWithExpectationsRestoresTerminatingPodsAfterRestart(t *testing.T) {
	deletionTimestamp := metav1.Now()
	group := podGangPods{
		nonTerminating: []*corev1.Pod{
			{ObjectMeta: metav1.ObjectMeta{UID: "live-1"}},
			{ObjectMeta: metav1.ObjectMeta{UID: "live-2"}},
			{ObjectMeta: metav1.ObjectMeta{UID: "live-3"}},
		},
		terminating: []*corev1.Pod{
			{ObjectMeta: metav1.ObjectMeta{UID: "terminating", DeletionTimestamp: &deletionTimestamp}},
		},
	}
	pclqObjMeta := metav1.ObjectMeta{Namespace: "default", Name: "test-pcs-0-w"}
	r := &_resource{expectationsStore: expect.NewExpectationsStore()}

	count, err := r.reconcileLivePodCountWithExpectations(pclqObjMeta, "test-pcs-0", group)
	require.NoError(t, err)

	assert.Equal(t, int32(3), count, "the terminating Pod must not count as live after an operator restart")
	key, err := pclqexp.PodGangScopedExpectationsStoreKey(pclqObjMeta, "test-pcs-0")
	require.NoError(t, err)
	assert.ElementsMatch(t, []types.UID{"terminating"}, r.expectationsStore.GetDeleteExpectations(key))
}
