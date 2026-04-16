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

package portforward

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestResolvePodViaEndpoints_Found(t *testing.T) {
	t.Parallel()

	clientset := fake.NewSimpleClientset(&corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-service",
			Namespace: "default",
		},
		Subsets: []corev1.EndpointSubset{
			{
				Addresses: []corev1.EndpointAddress{
					{
						IP: "10.0.0.1",
						TargetRef: &corev1.ObjectReference{
							Kind: "Pod",
							Name: "my-pod-abc123",
						},
					},
				},
			},
		},
	})

	podName, err := resolvePodViaEndpoints(context.Background(), clientset, "default", "my-service")
	require.NoError(t, err)
	assert.Equal(t, "my-pod-abc123", podName)
}

func TestResolvePodViaEndpoints_NoEndpoints(t *testing.T) {
	t.Parallel()

	clientset := fake.NewSimpleClientset()

	_, err := resolvePodViaEndpoints(context.Background(), clientset, "default", "missing-service")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing-service")
}

func TestResolvePodViaEndpoints_EmptySubsets(t *testing.T) {
	t.Parallel()

	clientset := fake.NewSimpleClientset(&corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-service",
			Namespace: "default",
		},
		Subsets: []corev1.EndpointSubset{},
	})

	_, err := resolvePodViaEndpoints(context.Background(), clientset, "default", "my-service")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no subsets")
}

func TestResolvePodViaEndpoints_OnlyNotReadyAddresses(t *testing.T) {
	t.Parallel()

	// NotReadyAddresses are not in Addresses — simulate with empty Addresses and
	// a non-nil Subsets entry (which is how K8s populates not-ready endpoints).
	clientset := fake.NewSimpleClientset(&corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-service",
			Namespace: "default",
		},
		Subsets: []corev1.EndpointSubset{
			{
				NotReadyAddresses: []corev1.EndpointAddress{
					{
						IP: "10.0.0.2",
						TargetRef: &corev1.ObjectReference{
							Kind: "Pod",
							Name: "not-ready-pod",
						},
					},
				},
				Addresses: []corev1.EndpointAddress{},
			},
		},
	})

	_, err := resolvePodViaEndpoints(context.Background(), clientset, "default", "my-service")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no ready pods")
}

func TestResolvePodViaEndpoints_NilTargetRef(t *testing.T) {
	t.Parallel()

	clientset := fake.NewSimpleClientset(&corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-service",
			Namespace: "default",
		},
		Subsets: []corev1.EndpointSubset{
			{
				Addresses: []corev1.EndpointAddress{
					{IP: "10.0.0.1", TargetRef: nil}, // nil TargetRef — skipped
				},
			},
		},
	})

	_, err := resolvePodViaEndpoints(context.Background(), clientset, "default", "my-service")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no ready pods")
}

func TestSession_CloseSafe(t *testing.T) {
	t.Parallel()

	stop := make(chan struct{})
	s := &Session{LocalPort: 12345, stop: stop}

	// Double-close must not panic.
	assert.NotPanics(t, func() {
		s.Close()
		s.Close()
	})
}

func TestSession_Addr(t *testing.T) {
	t.Parallel()

	s := &Session{LocalPort: 9090}
	assert.Equal(t, "localhost:9090", s.Addr())
}
