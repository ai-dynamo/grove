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

// Package config decodes the operator configuration of every supported operator.config.grove.io version.
package config

import (
	"fmt"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

// DecodeOperatorConfig parses raw YAML or JSON bytes of an operator.config.grove.io/v1beta1 or the deprecated
// operator.config.grove.io/v1alpha1 OperatorConfiguration, applies the defaults of its version, and returns it
// as v1beta1. The returned warnings name the deprecated version when the input uses it.
// No I/O or K8s API calls — safe to use in unit tests and tooling.
func DecodeOperatorConfig(data []byte) (*configv1beta1.OperatorConfiguration, []string, error) {
	configScheme := runtime.NewScheme()
	if err := configv1alpha1.AddToScheme(configScheme); err != nil {
		return nil, nil, fmt.Errorf("adding config v1alpha1 to scheme: %w", err)
	}
	if err := configv1beta1.AddToScheme(configScheme); err != nil {
		return nil, nil, fmt.Errorf("adding config v1beta1 to scheme: %w", err)
	}
	codecs := serializer.NewCodecFactory(configScheme)

	// The raw decoder reports the version of the input, which the converting decoder below does not.
	_, gvk, err := codecs.UniversalDeserializer().Decode(data, nil, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("decoding operator config: %w", err)
	}
	var warnings []string
	if gvk.GroupVersion() == configv1alpha1.SchemeGroupVersion {
		warnings = append(warnings, fmt.Sprintf("%s is deprecated; use %s", configv1alpha1.SchemeGroupVersion, configv1beta1.SchemeGroupVersion))
	}

	cfg := &configv1beta1.OperatorConfiguration{}
	if err := runtime.DecodeInto(codecs.UniversalDecoder(configv1beta1.SchemeGroupVersion), data, cfg); err != nil {
		return nil, nil, fmt.Errorf("decoding operator config: %w", err)
	}
	// Conversion from v1alpha1 leaves the TypeMeta empty.
	cfg.SetGroupVersionKind(configv1beta1.SchemeGroupVersion.WithKind("OperatorConfiguration"))
	return cfg, warnings, nil
}
