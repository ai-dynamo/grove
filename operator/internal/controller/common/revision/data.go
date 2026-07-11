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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
)

// Data is the persisted ControllerRevision.Data schema.
// Reconciliation code should consume a validated Revision instead.
type Data struct {
	UID            types.UID    `json:"uid"`
	Cliques        []CliqueData `json:"cliques"`
	GenerationHash string       `json:"generationHash"`
}

// CliqueData stores the raw PodClique template and hash.
type CliqueData struct {
	Name     string          `json:"name"`
	Template json.RawMessage `json:"template"`
	Hash     string          `json:"hash"`
}

// Revision is a validated view of the revision data.
type Revision struct {
	name           string
	uid            types.UID
	cliques        []CliqueData
	cliqueIndexes  map[string]int
	generationHash string
}

// DecodeRevision decodes and validates persisted ControllerRevision data.
func DecodeRevision(controllerRevision *appsv1.ControllerRevision) (*Revision, error) {
	data := Data{}
	if err := json.Unmarshal(controllerRevision.Data.Raw, &data); err != nil {
		return nil, fmt.Errorf("could not decode revision: %w", err)
	}
	cliqueIndexes, err := validateData(data)
	if err != nil {
		return nil, err
	}
	return &Revision{
		name:           controllerRevision.Name,
		uid:            data.UID,
		cliques:        data.Cliques,
		cliqueIndexes:  cliqueIndexes,
		generationHash: data.GenerationHash,
	}, nil
}

// MatchesOrderedCliques reports whether cliques have the same names, order, and
// semantically equal templates as the revision. Hashes are not compared.
func (r *Revision) MatchesOrderedCliques(cliques []CliqueData) (bool, error) {
	if len(r.cliques) != len(cliques) {
		return false, nil
	}
	for i, clique := range cliques {
		selected := r.cliques[i]
		if selected.Name != clique.Name {
			return false, nil
		}
		equal, err := semanticallyEqualPodTemplate(selected.Template, clique.Template)
		if err != nil || !equal {
			return false, err
		}
	}
	return true, nil
}

// Name returns the persisted controller revision name.
func (r *Revision) Name() string {
	return r.name
}

// UID returns the persisted PodCliqueSet UID.
func (r *Revision) UID() types.UID {
	return r.uid
}

// GenerationHash returns the persisted PodCliqueSet generation identity.
func (r *Revision) GenerationHash() string {
	return r.generationHash
}

// CliqueHash returns the persisted template identity selected for one clique.
func (r *Revision) CliqueHash(name string) (string, error) {
	index, ok := r.cliqueIndexes[name]
	if !ok {
		return "", fmt.Errorf("no selected pod template hash for cliqueName: %s", name)
	}
	return r.cliques[index].Hash, nil
}

// semanticallyEqualPodTemplate checks whether two PodTemplateSpecs,
// passed as raw JSON, are semantically equal.
func semanticallyEqualPodTemplate(left, right json.RawMessage) (bool, error) {
	var leftValue, rightValue corev1.PodTemplateSpec
	if err := json.Unmarshal(left, &leftValue); err != nil {
		return false, fmt.Errorf("could not decode stored revision content: %w", err)
	}
	if err := json.Unmarshal(right, &rightValue); err != nil {
		return false, fmt.Errorf("could not decode proposed revision content: %w", err)
	}
	return equality.Semantic.DeepEqual(leftValue, rightValue), nil
}

// validateData checks that all data for a revision is well-formed and maps
// cliques from name to index in the cliques list for faster lookup.
func validateData(data Data) (map[string]int, error) {
	if data.GenerationHash == "" {
		return nil, fmt.Errorf("revision has no generation hash")
	}
	cliqueIndexes := make(map[string]int, len(data.Cliques))
	for i, clique := range data.Cliques {
		if clique.Name == "" {
			return nil, fmt.Errorf("revision has a clique with no name")
		}
		if _, ok := cliqueIndexes[clique.Name]; ok {
			return nil, fmt.Errorf("revision has duplicate clique %s", clique.Name)
		}
		if !json.Valid(clique.Template) {
			return nil, fmt.Errorf("revision has invalid content for clique %s", clique.Name)
		}
		if clique.Hash == "" {
			return nil, fmt.Errorf("revision has no hash for clique %s", clique.Name)
		}
		cliqueIndexes[clique.Name] = i
	}
	return cliqueIndexes, nil
}
