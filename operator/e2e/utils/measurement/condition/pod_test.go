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
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPodConditions(t *testing.T) {
	t.Parallel()

	now := time.Now()
	readyTransition := metav1.NewTime(now.Add(-10 * time.Second))
	creation1 := metav1.NewTime(now.Add(-20 * time.Second))
	creation2 := metav1.NewTime(now.Add(-15 * time.Second))
	deleting := metav1.NewTime(now.Add(-5 * time.Second))

	podReady1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "p1",
			Namespace:         "default",
			Labels:            map[string]string{"grove.io/scale-test-run": "run1"},
			CreationTimestamp: creation1,
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: readyTransition},
			},
		},
	}
	podReady2 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "p2",
			Namespace:         "default",
			Labels:            map[string]string{"grove.io/scale-test-run": "run1"},
			CreationTimestamp: creation2,
			DeletionTimestamp: &deleting,
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: readyTransition},
			},
		},
	}
	podNotReady := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "p3",
			Namespace: "default",
			Labels:    map[string]string{"grove.io/scale-test-run": "run1"},
		},
	}

	clientset := fake.NewSimpleClientset(podReady1, podReady2, podNotReady)
	ctx := context.Background()
	selector := "grove.io/scale-test-run=run1"

	created := &PodsCreatedCondition{
		Clientset:     clientset,
		Namespace:     "default",
		LabelSelector: selector,
		ExpectedCount: 2,
	}
	createdMet, err := created.Met(ctx)
	if err != nil {
		t.Fatalf("PodsCreatedCondition.Met() error = %v", err)
	}
	if !createdMet {
		t.Fatalf("PodsCreatedCondition expected true, got false")
	}

	firstReady := &FirstPodReadyCondition{
		Clientset:     clientset,
		Namespace:     "default",
		LabelSelector: selector,
	}
	firstReadyMet, err := firstReady.Met(ctx)
	if err != nil {
		t.Fatalf("FirstPodReadyCondition.Met() error = %v", err)
	}
	if !firstReadyMet {
		t.Fatalf("FirstPodReadyCondition expected true, got false")
	}

	ready := &PodsReadyCondition{
		Clientset:     clientset,
		Namespace:     "default",
		LabelSelector: selector,
		ExpectedCount: 2,
	}
	readyMet, err := ready.Met(ctx)
	if err != nil {
		t.Fatalf("PodsReadyCondition.Met() error = %v", err)
	}
	if !readyMet {
		t.Fatalf("PodsReadyCondition expected true, got false")
	}

	terminating := &PodsTerminatingCondition{
		Clientset:     clientset,
		Namespace:     "default",
		LabelSelector: selector,
		ExpectedCount: 1,
	}
	termMet, err := terminating.Met(ctx)
	if err != nil {
		t.Fatalf("PodsTerminatingCondition.Met() error = %v", err)
	}
	if !termMet {
		t.Fatalf("PodsTerminatingCondition expected true, got false")
	}

	gone := &PodsGoneCondition{
		Clientset:     fake.NewSimpleClientset(),
		Namespace:     "default",
		LabelSelector: selector,
	}
	goneMet, err := gone.Met(ctx)
	if err != nil {
		t.Fatalf("PodsGoneCondition.Met() error = %v", err)
	}
	if !goneMet {
		t.Fatalf("PodsGoneCondition expected true, got false")
	}
}
