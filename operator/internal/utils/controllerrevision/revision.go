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

// Package controllerrevision encodes and compares PodCliqueSet templates stored in ControllerRevisions.
package controllerrevision

import (
	"encoding/json"
	"fmt"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
)

type revisionHeader struct {
	APIVersion string `json:"apiVersion"`
}

type revisionPayload struct {
	revisionHeader
	Template grovecorev1beta1.PodCliqueSetTemplateSpec `json:"podCliqueSetTemplateSpec"`
}

// NormalizeTemplate returns the canonical copy used for revision comparisons currently.
func NormalizeTemplate(template *grovecorev1beta1.PodCliqueSetTemplateSpec) *grovecorev1beta1.PodCliqueSetTemplateSpec {
	return template.DeepCopy()
}

// EncodeTemplate encodes a versioned PodCliqueSet template snapshot.
func EncodeTemplate(template *grovecorev1beta1.PodCliqueSetTemplateSpec) ([]byte, error) {
	payload := revisionPayload{
		revisionHeader: revisionHeader{APIVersion: grovecorev1beta1.SchemeGroupVersion.String()},
		Template:       *NormalizeTemplate(template),
	}
	data, err := json.Marshal(&payload)
	if err != nil {
		return nil, fmt.Errorf("could not encode PodCliqueSet revision: %w", err)
	}
	return data, nil
}

// DecodeTemplate decodes a versioned PodCliqueSet template snapshot.
//
// Decoding tolerates unknown fields so a revision written by a newer operator that stores
// additional payload fields can still be read by an older operator: the extra keys are ignored
// and the template is recovered. A breaking payload change must instead bump the apiVersion, which
// the version gate below rejects. Any additive field must therefore remain safe to read as its
// zero value here.
func DecodeTemplate(data []byte) (*grovecorev1beta1.PodCliqueSetTemplateSpec, error) {
	var payload revisionPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("could not decode PodCliqueSet revision: %w", err)
	}
	if payload.APIVersion != grovecorev1beta1.SchemeGroupVersion.String() {
		return nil, fmt.Errorf("unsupported PodCliqueSet revision API version %q", payload.APIVersion)
	}
	return NormalizeTemplate(&payload.Template), nil
}

// DataMatchesTemplate compares stored revision data with a normalized template.
func DataMatchesTemplate(data []byte, normalizedTemplate *grovecorev1beta1.PodCliqueSetTemplateSpec) (bool, error) {
	storedTemplate, err := DecodeTemplate(data)
	if err != nil {
		return false, err
	}
	return apiequality.Semantic.DeepEqual(storedTemplate, normalizedTemplate), nil
}
