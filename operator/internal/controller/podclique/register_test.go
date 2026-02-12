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
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// TestControllerConstants tests the controller constants
func TestControllerConstants(t *testing.T) {
	// Verifies that controller name is set correctly
	assert.Equal(t, "podclique-controller", controllerName)
}

func Test_hasPodStatusChanged(t *testing.T) {
	tests := []struct {
		name        string
		updateEvent event.UpdateEvent
		want        bool
	}{
		{
			name: "pod deletionTimestamp changed",
			updateEvent: event.UpdateEvent{
				ObjectOld: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						DeletionTimestamp: nil,
					},
				},
				ObjectNew: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						DeletionTimestamp: ptr.To(metav1.Now()),
					},
				},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, hasPodStatusChanged(tt.updateEvent), "hasPodStatusChanged(%v)", tt.updateEvent)
		})
	}
}
