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

package clients

import (
	"fmt"

	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveclient "github.com/ai-dynamo/grove/operator/client/clientset/versioned"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Clients bundles all Kubernetes client objects needed by e2e tests.
// All fields are goroutine-safe and a single Clients instance should be
// created per test run via NewClients, then shared across all test suites.
type Clients struct {
	Clientset     kubernetes.Interface
	DynamicClient dynamic.Interface
	GroveClient   groveclient.Interface
	RestMapper    meta.RESTMapper
	RestConfig    *rest.Config
	CRClient      client.Client
}

// NewClients creates all Kubernetes clients from a single rest.Config.
// The REST mapper uses a memory-cached discovery client so API group
// lookups are performed once and reused across all YAML apply operations.
func NewClients(restConfig *rest.Config) (*Clients, error) {
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset: %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery client: %w", err)
	}
	cachedDiscoveryClient := memory.NewMemCacheClient(discoveryClient)
	restMapper := restmapper.NewDeferredDiscoveryRESTMapper(cachedDiscoveryClient)

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("add client-go scheme: %w", err)
	}
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("add Grove scheme: %w", err)
	}
	crClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("create controller-runtime client: %w", err)
	}

	groveClient, err := groveclient.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create Grove typed client: %w", err)
	}

	return &Clients{
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		GroveClient:   groveClient,
		RestMapper:    restMapper,
		RestConfig:    restConfig,
		CRClient:      crClient,
	}, nil
}

// NewCRClient creates a controller-runtime client with the Grove scheme registered.
// This is useful when only a CR client is needed without the full Clients bundle.
func NewCRClient(restConfig *rest.Config) (client.Client, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("add client-go scheme: %w", err)
	}
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("add Grove scheme: %w", err)
	}

	cl, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("create controller-runtime client: %w", err)
	}

	return cl, nil
}
