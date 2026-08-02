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

package revision

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeSelectedRevision(t *testing.T) {
	cliques := []CliqueData{
		{Name: "worker", Template: json.RawMessage(`{"metadata":{"labels":{"role":"worker"}},"spec":{"containers":[]}}`), Hash: "worker-template"},
		{Name: "sidecar", Template: json.RawMessage(`{"metadata":{"labels":{"role":"sidecar"}},"spec":{"containers":[]}}`), Hash: "sidecar-template"},
	}
	raw := mustMarshalData(t, Data{
		Cliques:        cliques,
		GenerationHash: "generation",
	})
	assert.NotContains(t, string(raw), `"desired"`)
	assert.NotContains(t, string(raw), `"cliqueHashes"`)

	selected, err := DecodeSelectedRevision(raw)
	require.NoError(t, err)
	assert.Equal(t, "generation", selected.GenerationHash())
	hash, err := selected.CliqueHash("worker")
	require.NoError(t, err)
	assert.Equal(t, "worker-template", hash)
	hash, err = selected.CliqueHash("sidecar")
	require.NoError(t, err)
	assert.Equal(t, "sidecar-template", hash)
	_, err = selected.CliqueHash("missing")
	require.Error(t, err)
}

func TestSelectedRevisionMatchesOrderedCliques(t *testing.T) {
	selected, err := DecodeSelectedRevision(mustMarshalData(t, Data{
		Cliques: []CliqueData{
			{Name: "worker", Template: json.RawMessage(`{"metadata":{"labels":{"role":"worker"}}}`), Hash: "worker-template"},
			{Name: "sidecar", Template: json.RawMessage(`{"metadata":{"labels":{"role":"sidecar"}}}`), Hash: "sidecar-template"},
		},
		GenerationHash: "generation",
	}))
	require.NoError(t, err)

	tests := []struct {
		name    string
		cliques []CliqueData
		want    bool
	}{
		{
			name: "matches names order and templates while ignoring hashes",
			cliques: []CliqueData{
				{Name: "worker", Template: json.RawMessage(`{"metadata":{"labels":{"role":"worker"}}}`)},
				{Name: "sidecar", Template: json.RawMessage(`{"metadata":{"labels":{"role":"sidecar"}}}`)},
			},
			want: true,
		},
		{
			name: "different order",
			cliques: []CliqueData{
				{Name: "sidecar", Template: json.RawMessage(`{"metadata":{"labels":{"role":"sidecar"}}}`)},
				{Name: "worker", Template: json.RawMessage(`{"metadata":{"labels":{"role":"worker"}}}`)},
			},
		},
		{
			name: "different template",
			cliques: []CliqueData{
				{Name: "worker", Template: json.RawMessage(`{"metadata":{"labels":{"role":"changed"}}}`)},
				{Name: "sidecar", Template: json.RawMessage(`{"metadata":{"labels":{"role":"sidecar"}}}`)},
			},
		},
		{
			name:    "different count",
			cliques: []CliqueData{{Name: "worker", Template: json.RawMessage(`{"metadata":{"labels":{"role":"worker"}}}`)}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			equal, err := selected.MatchesOrderedCliques(tt.cliques)
			require.NoError(t, err)
			assert.Equal(t, tt.want, equal)
		})
	}

	equal, err := selected.MatchesOrderedCliques([]CliqueData{
		{Name: "worker", Template: json.RawMessage(`{`)},
		{Name: "sidecar", Template: json.RawMessage(`{}`)},
	})
	assert.False(t, equal)
	require.Error(t, err)
}

func TestSelectedRevisionMatchingCliqueHash(t *testing.T) {
	selected, err := DecodeSelectedRevision(mustMarshalData(t, Data{
		Cliques: []CliqueData{{
			Name:     "worker",
			Template: json.RawMessage(`{"metadata":{"labels":{"role":"worker"}}}`),
			Hash:     "worker-template",
		}},
		GenerationHash: "generation",
	}))
	require.NoError(t, err)

	hash, matches, err := selected.MatchingCliqueHash(CliqueData{
		Name:     "worker",
		Template: json.RawMessage(`{"metadata":{"labels":{"role":"worker"},"creationTimestamp":null}}`),
	})
	require.NoError(t, err)
	assert.True(t, matches)
	assert.Equal(t, "worker-template", hash)

	for _, clique := range []CliqueData{
		{Name: "missing", Template: json.RawMessage(`{}`)},
		{Name: "worker", Template: json.RawMessage(`{"metadata":{"labels":{"role":"changed"}}}`)},
	} {
		hash, matches, err = selected.MatchingCliqueHash(clique)
		require.NoError(t, err)
		assert.False(t, matches)
		assert.Empty(t, hash)
	}

	_, matches, err = selected.MatchingCliqueHash(CliqueData{Name: "worker", Template: json.RawMessage(`{`)})
	assert.False(t, matches)
	require.Error(t, err)
}

func TestSemanticallyEqualPodTemplate(t *testing.T) {
	tests := []struct {
		name      string
		left      json.RawMessage
		right     json.RawMessage
		wantEqual bool
		wantError bool
	}{
		{
			name:      "normalizes representation differences",
			left:      json.RawMessage(`{"metadata":{"labels":{"app":"worker"},"creationTimestamp":null},"spec":{"containers":[{"name":"worker","image":"v1","resources":{}}]}}`),
			right:     json.RawMessage(`{"spec":{"containers":[{"resources":{},"image":"v1","name":"worker"}]},"metadata":{"labels":{"app":"worker"}}}`),
			wantEqual: true,
		},
		{
			name:  "detects semantic changes",
			left:  json.RawMessage(`{"spec":{"containers":[{"name":"worker","image":"v1"}]}}`),
			right: json.RawMessage(`{"spec":{"containers":[{"name":"worker","image":"v2"}]}}`),
		},
		{
			name:      "fails closed for malformed stored content",
			left:      json.RawMessage(`{`),
			right:     json.RawMessage(`{}`),
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			equal, err := semanticallyEqualPodTemplate(tt.left, tt.right)
			if tt.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantEqual, equal)
		})
	}
}

func TestDecodeSelectedRevisionRejectsInvalidData(t *testing.T) {
	tests := []struct {
		name string
		raw  func(*testing.T) []byte
	}{
		{
			name: "malformed JSON",
			raw:  func(*testing.T) []byte { return []byte(`{`) },
		},
		{
			name: "empty generation hash",
			raw: func(t *testing.T) []byte {
				return mustMarshalData(t, validData(func(data *Data) { data.GenerationHash = "" }))
			},
		},
		{
			name: "missing clique template",
			raw: func(*testing.T) []byte {
				return []byte(`{"cliques":[{"name":"worker","hash":"template"}],"generationHash":"generation"}`)
			},
		},
		{
			name: "empty clique name",
			raw: func(t *testing.T) []byte {
				return mustMarshalData(t, validData(func(data *Data) {
					data.Cliques[0].Name = ""
				}))
			},
		},
		{
			name: "duplicate clique name",
			raw: func(t *testing.T) []byte {
				return mustMarshalData(t, validData(func(data *Data) {
					data.Cliques = append(data.Cliques, data.Cliques[0])
				}))
			},
		},
		{
			name: "empty clique hash",
			raw: func(t *testing.T) []byte {
				return mustMarshalData(t, validData(func(data *Data) { data.Cliques[0].Hash = "" }))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeSelectedRevision(tt.raw(t))
			require.Error(t, err)
		})
	}
}

func validData(mutate func(*Data)) Data {
	data := Data{
		Cliques:        []CliqueData{{Name: "worker", Template: json.RawMessage(`{}`), Hash: "template"}},
		GenerationHash: "generation",
	}
	mutate(&data)
	return data
}

func mustMarshalData(t *testing.T, data Data) []byte {
	t.Helper()
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	return raw
}
