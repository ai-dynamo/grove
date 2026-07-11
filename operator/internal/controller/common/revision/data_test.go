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

package revision

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

func TestSelectedRevisionMatchesOrderedCliques(t *testing.T) {
	raw, err := json.Marshal(Data{
		UID: types.UID("test-uid"),
		Cliques: []CliqueData{
			{Name: "worker", Template: json.RawMessage(`{"metadata":{"labels":{"role":"worker"}}}`), Hash: "worker-template"},
			{Name: "sidecar", Template: json.RawMessage(`{"metadata":{"labels":{"role":"sidecar"}}}`), Hash: "sidecar-template"},
		},
		GenerationHash: "generation",
	})
	require.NoError(t, err)

	controllerRevision := &appsv1.ControllerRevision{
		ObjectMeta: v1.ObjectMeta{Name: "test-cr"},
		Data:       runtime.RawExtension{Raw: raw},
	}

	selected, err := DecodeRevision(controllerRevision)
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

func TestDecodeSelectedRevision(t *testing.T) {
	raw, err := json.Marshal(Data{
		UID: types.UID("test-uid"),
		Cliques: []CliqueData{
			{Name: "worker", Template: json.RawMessage(`{"metadata":{"labels":{"role":"worker"}},"spec":{"containers":[]}}`), Hash: "worker-template"},
			{Name: "sidecar", Template: json.RawMessage(`{"metadata":{"labels":{"role":"sidecar"}},"spec":{"containers":[]}}`), Hash: "sidecar-template"},
		},
		GenerationHash: "generation",
	})
	require.NoError(t, err)

	controllerRevision := &appsv1.ControllerRevision{
		ObjectMeta: v1.ObjectMeta{Name: "test-cr"},
		Data:       runtime.RawExtension{Raw: raw},
	}

	selected, err := DecodeRevision(controllerRevision)
	require.NoError(t, err)

	assert.Equal(t, "test-cr", selected.Name())
	assert.Equal(t, types.UID("test-uid"), selected.UID())
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

func TestDecodeSelectedRevisionRejectsInvalidData(t *testing.T) {
	validData := func() Data {
		return Data{
			UID:            types.UID("test-uid"),
			Cliques:        []CliqueData{{Name: "worker", Template: json.RawMessage(`{}`), Hash: "template"}},
			GenerationHash: "generation",
		}
	}

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
				data := validData()
				data.GenerationHash = ""

				raw, err := json.Marshal(data)
				require.NoError(t, err)
				return raw
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
				data := validData()
				data.Cliques[0].Name = ""

				raw, err := json.Marshal(data)
				require.NoError(t, err)
				return raw
			},
		},
		{
			name: "duplicate clique name",
			raw: func(t *testing.T) []byte {
				data := validData()
				data.Cliques = append(data.Cliques, data.Cliques[0])

				raw, err := json.Marshal(data)
				require.NoError(t, err)
				return raw
			},
		},
		{
			name: "empty clique hash",
			raw: func(t *testing.T) []byte {
				data := validData()
				data.Cliques[0].Hash = ""

				raw, err := json.Marshal(data)
				require.NoError(t, err)
				return raw
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controllerRevision := &appsv1.ControllerRevision{
				ObjectMeta: v1.ObjectMeta{Name: "test-cr"},
				Data:       runtime.RawExtension{Raw: tt.raw(t)},
			}

			_, err := DecodeRevision(controllerRevision)
			require.Error(t, err)
		})
	}
}
