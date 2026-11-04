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

// Package conversion serves the CRD conversion webhook for the grove.io kinds that are served at more than
// one version with a webhook conversion strategy. v1beta1 is the hub version and v1alpha1 is a spoke.
package conversion

import (
	"context"
	"fmt"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	ctrlconversion "sigs.k8s.io/controller-runtime/pkg/webhook/conversion"
)

const (
	// Name is the name of the conversion webhook.
	Name = "conversion-webhook"
	// WebhookPath is the path the conversion webhook is served at. The CRDs' spec.conversion points at it.
	WebhookPath = "/convert"
)

// converterFactory builds the converter of one kind from the manager's scheme.
type converterFactory struct {
	groupKind schema.GroupKind
	// resource is the plural resource name of the kind, which names its CRD.
	resource     string
	newConverter func(*runtime.Scheme) (ctrlconversion.Converter, error)
}

// Register registers a hub-and-spoke converter for every converted kind with the manager's converter
// registry, and serves the conversion webhook at WebhookPath. The other grove.io kinds have the same schema in
// every version, use the None conversion strategy and are not registered.
func Register(mgr manager.Manager) error {
	registry := mgr.GetConverterRegistry()
	for _, factory := range converterFactories() {
		converter, err := factory.newConverter(mgr.GetScheme())
		if err != nil {
			return fmt.Errorf("failed to create converter for %s: %w", factory.groupKind, err)
		}
		if err := registry.RegisterConverter(factory.groupKind, newInstrumentedConverter(factory.groupKind.Kind, converter)); err != nil {
			return err
		}
	}
	mgr.GetWebhookServer().Register(WebhookPath, ctrlconversion.NewWebhookHandler(mgr.GetScheme(), registry))
	return nil
}

// ConvertedGroupKinds returns the grove.io kinds served through the conversion webhook.
func ConvertedGroupKinds() []schema.GroupKind {
	factories := converterFactories()
	groupKinds := make([]schema.GroupKind, 0, len(factories))
	for _, factory := range factories {
		groupKinds = append(groupKinds, factory.groupKind)
	}
	return groupKinds
}

// ConvertedCRDNames returns the names of the CRDs whose spec.conversion points at the conversion webhook.
func ConvertedCRDNames() []string {
	factories := converterFactories()
	names := make([]string, 0, len(factories))
	for _, factory := range factories {
		names = append(names, factory.resource+"."+factory.groupKind.Group)
	}
	return names
}

func converterFactories() []converterFactory {
	return []converterFactory{
		{
			groupKind: grovecorev1beta1.Kind("PodCliqueSet"),
			resource:  "podcliquesets",
			newConverter: ctrlconversion.NewHubSpokeConverter(&grovecorev1beta1.PodCliqueSet{},
				ctrlconversion.NewSpokeConverter(&grovecorev1alpha1.PodCliqueSet{},
					func(_ context.Context, hub *grovecorev1beta1.PodCliqueSet, spoke *grovecorev1alpha1.PodCliqueSet) error {
						return grovecorev1alpha1.Convert_v1beta1_PodCliqueSet_To_v1alpha1_PodCliqueSet(hub, spoke, nil)
					},
					func(_ context.Context, spoke *grovecorev1alpha1.PodCliqueSet, hub *grovecorev1beta1.PodCliqueSet) error {
						return grovecorev1alpha1.Convert_v1alpha1_PodCliqueSet_To_v1beta1_PodCliqueSet(spoke, hub, nil)
					})),
		},
	}
}
