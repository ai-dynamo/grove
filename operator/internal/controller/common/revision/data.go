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

// Package revision defines the immutable data stored in a PodCliqueSet ControllerRevision.
package revision

import (
	"encoding/json"
	"fmt"
	"slices"
)

// DataVersion is the current ControllerRevision data schema version.
const DataVersion = 1

// Data is the persisted ControllerRevision.Data schema. Revision construction code populates it;
// reconciliation code should consume a validated SelectedRevision instead.
type Data struct {
	Version        int                        `json:"version"`
	Desired        json.RawMessage            `json:"desired"`
	Cliques        map[string]json.RawMessage `json:"cliques"`
	GenerationHash string                     `json:"generationHash"`
	CliqueHashes   map[string]string          `json:"cliqueHashes"`
}

// SelectedRevision is a validated, read-oriented view of persisted revision data.
// It deliberately exposes no maps or owned byte slices so reconciliation code cannot mutate
// the selected rollout identity after loading it.
type SelectedRevision struct {
	data Data
}

// DecodeSelectedRevision decodes and validates persisted ControllerRevision data.
func DecodeSelectedRevision(raw []byte) (*SelectedRevision, error) {
	data := Data{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("could not decode selected revision: %w", err)
	}
	if err := validateData(data); err != nil {
		return nil, err
	}
	return &SelectedRevision{data: data}, nil
}

// Desired returns a copy of the complete desired rollout content.
func (r *SelectedRevision) Desired() json.RawMessage {
	return slices.Clone(r.data.Desired)
}

// Clique returns a copy of the desired rollout content for one clique.
func (r *SelectedRevision) Clique(name string) (json.RawMessage, bool) {
	clique, ok := r.data.Cliques[name]
	return slices.Clone(clique), ok
}

// GenerationHash returns the persisted PodCliqueSet generation identity.
func (r *SelectedRevision) GenerationHash() string {
	return r.data.GenerationHash
}

// CliqueHash returns the persisted template identity selected for one clique.
func (r *SelectedRevision) CliqueHash(name string) (string, error) {
	hash, ok := r.data.CliqueHashes[name]
	if !ok {
		return "", fmt.Errorf("no selected pod template hash for cliqueName: %s", name)
	}
	return hash, nil
}

func validateData(data Data) error {
	if data.Version != DataVersion {
		return fmt.Errorf("unsupported selected revision version: %d", data.Version)
	}
	if data.GenerationHash == "" {
		return fmt.Errorf("selected revision has no generation hash")
	}
	if !json.Valid(data.Desired) {
		return fmt.Errorf("selected revision has invalid desired content")
	}
	if len(data.Cliques) != len(data.CliqueHashes) {
		return fmt.Errorf("selected revision has different clique content and hash counts")
	}
	for name, clique := range data.Cliques {
		if !json.Valid(clique) {
			return fmt.Errorf("selected revision has invalid content for clique %s", name)
		}
		if data.CliqueHashes[name] == "" {
			return fmt.Errorf("selected revision has no hash for clique %s", name)
		}
	}
	return nil
}
