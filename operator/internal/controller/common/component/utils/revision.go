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

package utils

import (
	"context"
	"encoding/json"
	"fmt"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PodCliqueSetRevisionDataVersion is the current ControllerRevision data schema.
const PodCliqueSetRevisionDataVersion = 1

// PodCliqueSetRevisionData is the immutable selected rollout revision stored in ControllerRevision.Data.
type PodCliqueSetRevisionData struct {
	Version        int                        `json:"version"`
	Desired        json.RawMessage            `json:"desired"`
	Cliques        map[string]json.RawMessage `json:"cliques"`
	GenerationHash string                     `json:"generationHash"`
	CliqueHashes   map[string]string          `json:"cliqueHashes"`
}

// GetPodCliqueSetRevisionData loads and validates the ControllerRevision selected by the PodCliqueSet.
func GetPodCliqueSetRevisionData(ctx context.Context, cl client.Client, pcs *grovecorev1alpha1.PodCliqueSet) (*PodCliqueSetRevisionData, error) {
	if pcs.Status.CurrentRevision == nil || *pcs.Status.CurrentRevision == "" {
		return nil, fmt.Errorf("PodCliqueSet %v has no selected ControllerRevision", client.ObjectKeyFromObject(pcs))
	}
	revision := &appsv1.ControllerRevision{}
	if err := cl.Get(ctx, client.ObjectKey{Namespace: pcs.Namespace, Name: *pcs.Status.CurrentRevision}, revision); err != nil {
		return nil, err
	}
	if !metav1.IsControlledBy(revision, pcs) {
		return nil, fmt.Errorf("ControllerRevision %v is not controlled by PodCliqueSet %v", client.ObjectKeyFromObject(revision), client.ObjectKeyFromObject(pcs))
	}
	data := &PodCliqueSetRevisionData{}
	if err := json.Unmarshal(revision.Data.Raw, data); err != nil {
		return nil, fmt.Errorf("could not decode ControllerRevision %v: %w", client.ObjectKeyFromObject(revision), err)
	}
	if data.Version != PodCliqueSetRevisionDataVersion || data.GenerationHash == "" || !json.Valid(data.Desired) ||
		len(data.Cliques) != len(data.CliqueHashes) {
		return nil, fmt.Errorf("ControllerRevision %v has invalid revision data", client.ObjectKeyFromObject(revision))
	}
	for name, clique := range data.Cliques {
		if !json.Valid(clique) || data.CliqueHashes[name] == "" {
			return nil, fmt.Errorf("ControllerRevision %v has invalid clique %s", client.ObjectKeyFromObject(revision), name)
		}
	}
	if pcs.Status.CurrentGenerationHash == nil || *pcs.Status.CurrentGenerationHash != data.GenerationHash {
		return nil, fmt.Errorf("PodCliqueSet %v generation identity does not match ControllerRevision %v", client.ObjectKeyFromObject(pcs), client.ObjectKeyFromObject(revision))
	}
	return data, nil
}
