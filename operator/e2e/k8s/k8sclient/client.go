//go:build e2e

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

package k8sclient

import (
	"context"
	"fmt"
	"reflect"

	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	kaitopologyv1alpha1 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/kai/v1alpha1"
	kaischedulingv2alpha2 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/scheduling/v2alpha2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crlog "sigs.k8s.io/controller-runtime/pkg/log"
)

// Client is the unified Kubernetes client for e2e tests.
// It embeds client.Client for all typed and unstructured operations,
// and keeps a private clientset for capabilities client.Client lacks
// (log streaming, pod watching).
type Client struct {
	client.Client
	RestConfig *rest.Config
	clientset  kubernetes.Interface
}

// schemeBuilder registers all types needed by e2e tests.
var schemeBuilder = runtime.SchemeBuilder{
	clientgoscheme.AddToScheme,
	corev1alpha1.AddToScheme,
	groveschedulerv1alpha1.AddToScheme,
	kaischedulingv2alpha2.AddToScheme,
	kaitopologyv1alpha1.AddToScheme,
}

// newScheme creates a runtime.Scheme with all e2e types registered.
func newScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	if err := schemeBuilder.AddToScheme(scheme); err != nil {
		return nil, err
	}
	return scheme, nil
}

func init() {
	// Initialize the controller-runtime logger to suppress the
	// "log.SetLogger(...) was never called" warning that fires when
	// client.Client handles API warning headers.
	crlog.SetLogger(klog.Background())
}

// New creates a Client from a rest.Config.
func New(restConfig *rest.Config) (*Client, error) {
	scheme, err := newScheme()
	if err != nil {
		return nil, fmt.Errorf("build scheme: %w", err)
	}

	cl, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("create controller-runtime client: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create clientset: %w", err)
	}

	return &Client{
		Client:     cl,
		RestConfig: restConfig,
		clientset:  clientset,
	}, nil
}

// GetLogs returns a log stream request for a pod container.
func (k *Client) GetLogs(namespace, podName string, opts *corev1.PodLogOptions) *rest.Request {
	return k.clientset.CoreV1().Pods(namespace).GetLogs(podName, opts)
}

// WatchPods starts a watch on pods in the given namespace.
func (k *Client) WatchPods(ctx context.Context, namespace string, opts metav1.ListOptions) (watch.Interface, error) {
	return k.clientset.CoreV1().Pods(namespace).Watch(ctx, opts)
}

// Getter returns a waiter.GetFunc that uses client.Client.Get for the given type and namespace.
// This is a free function because Go methods cannot have type parameters.
func Getter[T client.Object](k *Client, namespace string) waiter.GetFunc[T] {
	return func(ctx context.Context, name string, _ metav1.GetOptions) (T, error) {
		obj := newInstance[T]()
		key := types.NamespacedName{Namespace: namespace, Name: name}
		if err := k.Get(ctx, key, obj); err != nil {
			var zero T
			return zero, err
		}
		return obj, nil
	}
}

// newInstance creates a new zero-value instance of a pointer type T.
// For example, newInstance[*v1.Node]() returns &v1.Node{}.
func newInstance[T client.Object]() T {
	var zero T
	t := reflect.TypeOf(zero).Elem()
	return reflect.New(t).Interface().(T)
}
