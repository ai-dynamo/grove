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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
)

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1alpha1.AddToScheme(s)
	return s
}

func TestPCSDeletedCondition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		objs    []runtime.Object
		want    bool
		wantErr bool
	}{
		{
			name: "met when PCS does not exist",
			objs: nil,
			want: true,
		},
		{
			name: "not met when PCS still exists",
			objs: []runtime.Object{&corev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: "my-pcs", Namespace: "default"},
			}},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithRuntimeObjects(tc.objs...).Build()
			cond := &PCSDeletedCondition{
				Client:    cl,
				Name:      "my-pcs",
				Namespace: "default",
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

func TestPCSAvailableCondition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		available     int32
		expectedCount int32
		want          bool
		wantErr       bool
	}{
		{
			name:          "met when enough available replicas",
			available:     3,
			expectedCount: 3,
			want:          true,
		},
		{
			name:          "met when more than expected",
			available:     5,
			expectedCount: 3,
			want:          true,
		},
		{
			name:          "not met when fewer available",
			available:     1,
			expectedCount: 3,
			want:          false,
		},
		{
			name:          "met when zero expected",
			available:     0,
			expectedCount: 0,
			want:          true,
		},
		{
			name:          "error on negative expected",
			available:     0,
			expectedCount: -1,
			wantErr:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pcs := &corev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: "my-pcs", Namespace: "default"},
				Status:     corev1alpha1.PodCliqueSetStatus{AvailableReplicas: tc.available},
			}

			cl := fake.NewClientBuilder().
				WithScheme(newTestScheme()).
				WithRuntimeObjects(pcs).
				WithStatusSubresource(pcs).
				Build()

			cond := &PCSAvailableCondition{
				Client:        cl,
				Name:          "my-pcs",
				Namespace:     "default",
				ExpectedCount: tc.expectedCount,
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
