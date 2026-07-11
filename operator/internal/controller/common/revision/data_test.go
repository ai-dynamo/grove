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
	raw := mustMarshalData(t, Data{
		Version:        DataVersion,
		Desired:        json.RawMessage(`[{"spec":{"containers":[]}}]`),
		Cliques:        map[string]json.RawMessage{"worker": json.RawMessage(`{"spec":{"containers":[]}}`)},
		GenerationHash: "generation",
		CliqueHashes:   map[string]string{"worker": "template"},
	})

	selected, err := DecodeSelectedRevision(raw)
	require.NoError(t, err)
	assert.Equal(t, "generation", selected.GenerationHash())
	hash, err := selected.CliqueHash("worker")
	require.NoError(t, err)
	assert.Equal(t, "template", hash)
	_, err = selected.CliqueHash("missing")
	require.Error(t, err)

	desired := selected.Desired()
	clique, ok := selected.Clique("worker")
	require.True(t, ok)
	desired[0] = 'x'
	clique[0] = 'x'
	assert.JSONEq(t, `[{"spec":{"containers":[]}}]`, string(selected.Desired()))
	clique, ok = selected.Clique("worker")
	require.True(t, ok)
	assert.JSONEq(t, `{"spec":{"containers":[]}}`, string(clique))
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
			name: "unsupported version",
			raw: func(t *testing.T) []byte {
				return mustMarshalData(t, validData(func(data *Data) { data.Version++ }))
			},
		},
		{
			name: "empty generation hash",
			raw: func(t *testing.T) []byte {
				return mustMarshalData(t, validData(func(data *Data) { data.GenerationHash = "" }))
			},
		},
		{
			name: "missing desired content",
			raw: func(*testing.T) []byte {
				return []byte(`{"version":1,"cliques":{"worker":{}},"generationHash":"generation","cliqueHashes":{"worker":"template"}}`)
			},
		},
		{
			name: "clique and hash sets differ",
			raw: func(t *testing.T) []byte {
				return mustMarshalData(t, validData(func(data *Data) {
					data.CliqueHashes = map[string]string{"other": "template"}
				}))
			},
		},
		{
			name: "empty clique hash",
			raw: func(t *testing.T) []byte {
				return mustMarshalData(t, validData(func(data *Data) { data.CliqueHashes["worker"] = "" }))
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
		Version:        DataVersion,
		Desired:        json.RawMessage(`[]`),
		Cliques:        map[string]json.RawMessage{"worker": json.RawMessage(`{}`)},
		GenerationHash: "generation",
		CliqueHashes:   map[string]string{"worker": "template"},
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
