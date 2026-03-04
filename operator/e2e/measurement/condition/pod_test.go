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

package condition

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newPodScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	return s
}

func TestPodsCreatedCondition(t *testing.T) {
	t.Parallel()

	readyPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "p1",
			Namespace: "default",
			Labels:    map[string]string{"app": "test"},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
	pendingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "p2",
			Namespace: "default",
			Labels:    map[string]string{"app": "test"},
		},
	}

	tests := []struct {
		name     string
		pods     []runtime.Object
		expected int
		want     bool
		wantErr  bool
	}{
		{
			name:     "met when enough pods exist",
			pods:     []runtime.Object{readyPod, pendingPod},
			expected: 2,
			want:     true,
		},
		{
			name:     "not met when too few pods",
			pods:     []runtime.Object{readyPod},
			expected: 3,
			want:     false,
		},
		{
			name:     "met when zero expected",
			pods:     nil,
			expected: 0,
			want:     true,
		},
		{
			name:     "error on negative expected",
			pods:     nil,
			expected: -1,
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cl := fake.NewClientBuilder().WithScheme(newPodScheme()).WithRuntimeObjects(tc.pods...).Build()
			cond := &PodsCreatedCondition{
				Client:        cl,
				Namespace:     "default",
				LabelSelector: "app=test",
				ExpectedCount: tc.expected,
			}

			got, err := cond.Met(context.Background())
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Met() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPodsReadyCondition(t *testing.T) {
	t.Parallel()

	readyPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "p1",
			Namespace: "default",
			Labels:    map[string]string{"app": "test"},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
	pendingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "p2",
			Namespace: "default",
			Labels:    map[string]string{"app": "test"},
		},
	}

	tests := []struct {
		name     string
		pods     []runtime.Object
		expected int
		want     bool
		wantErr  bool
	}{
		{
			name:     "met when enough ready pods",
			pods:     []runtime.Object{readyPod},
			expected: 1,
			want:     true,
		},
		{
			name:     "not met when not enough ready",
			pods:     []runtime.Object{readyPod, pendingPod},
			expected: 2,
			want:     false,
		},
		{
			name:     "met when zero expected",
			pods:     nil,
			expected: 0,
			want:     true,
		},
		{
			name:     "error on negative expected",
			pods:     nil,
			expected: -1,
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cl := fake.NewClientBuilder().WithScheme(newPodScheme()).WithRuntimeObjects(tc.pods...).Build()
			cond := &PodsReadyCondition{
				Client:        cl,
				Namespace:     "default",
				LabelSelector: "app=test",
				ExpectedCount: tc.expected,
			}

			got, err := cond.Met(context.Background())
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Met() = %v, want %v", got, tc.want)
			}
		})
	}
}
