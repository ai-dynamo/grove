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

// Package revision defines the immutable data stored in a PodCliqueSet ControllerRevision.
package revision

import (
	"encoding/json"
	"fmt"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/utils/podtemplatehash"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
)

// PodCliqueSetData returns the revision Data for a given PodCliqueSet.
func PodCliqueSetData(pcs *grovecorev1alpha1.PodCliqueSet) (Data, error) {
	templates := lo.Map(pcs.Spec.Template.Cliques, func(clique *grovecorev1alpha1.PodCliqueTemplateSpec, _ int) *corev1.PodTemplateSpec {
		return podtemplatehash.PodTemplateSpec(pcs, clique)
	})

	cliques := make([]CliqueData, len(pcs.Spec.Template.Cliques))
	for i, clique := range pcs.Spec.Template.Cliques {
		cliques[i].Name = clique.Name
		template, err := json.Marshal(templates[i])
		if err != nil {
			return Data{}, fmt.Errorf("could not serialize clique template spec %q: %w", clique.Name, err)
		}
		cliques[i].Template = template
		cliques[i].Hash = podtemplatehash.Compute(templates[i])
	}

	return Data{
		UID:            pcs.UID,
		Cliques:        cliques,
		GenerationHash: podtemplatehash.Compute(templates...),
	}, nil
}
