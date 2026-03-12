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

package v1alpha1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

// DecodeOperatorConfig parses raw YAML or JSON bytes into an OperatorConfiguration,
// applying scheme defaults. Uses the same codec as the operator at startup.
// No I/O or K8s API calls — safe to use in unit tests and tooling.
func DecodeOperatorConfig(data []byte) (*OperatorConfiguration, error) {
	configScheme := runtime.NewScheme()
	if err := AddToScheme(configScheme); err != nil {
		return nil, fmt.Errorf("adding config to scheme: %w", err)
	}
	configDecoder := serializer.NewCodecFactory(configScheme).UniversalDecoder()

	cfg := &OperatorConfiguration{}
	if err := runtime.DecodeInto(configDecoder, data, cfg); err != nil {
		return nil, fmt.Errorf("decoding operator config: %w", err)
	}
	return cfg, nil
}
