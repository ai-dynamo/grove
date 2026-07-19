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

package podgangmap

import (
	"context"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestGetExistingResourceNames(t *testing.T) {
	pcs := pcsWithCoherentUpdateStrategy(testHash).Build()
	// A PodGangMap owned by a different PodCliqueSet: different name (so different selector labels)
	// and a different owner UID.
	otherPGM := testutils.NewPodGangMapBuilder("other-pcs", testNamespace, types.UID("other-uid"), 0).
		WithEntries(testutils.NewPodGangEntryBuilder("pg-0", testHash, "e0").WithEpochAnchor(true).Build()).Build()

	tests := []struct {
		name      string
		pgms      []client.Object
		wantNames []string
	}{
		{
			name:      "no PodGangMaps yields empty",
			wantNames: nil,
		},
		{
			name:      "returns names of PodGangMaps owned by the PodCliqueSet",
			pgms:      []client.Object{pgmWithEpochEntry(0, "e0"), pgmWithEpochEntry(1, "e0")},
			wantNames: []string{"my-pcs-0", "my-pcs-1"},
		},
		{
			name:      "excludes PodGangMaps of a different PodCliqueSet",
			pgms:      []client.Object{pgmWithEpochEntry(0, "e0"), otherPGM},
			wantNames: []string{"my-pcs-0"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newResource(newFakeClient(append([]client.Object{pcs}, tc.pgms...)...))
			names, err := r.GetExistingResourceNames(context.Background(), logr.Discard(), pcs.ObjectMeta)
			require.NoError(t, err)
			assert.ElementsMatch(t, tc.wantNames, names)
		})
	}
}

func TestDelete(t *testing.T) {
	pcs := pcsWithCoherentUpdateStrategy(testHash).Build()
	owned := pgmWithEpochEntry(0, "e0")
	other := testutils.NewPodGangMapBuilder("other-pcs", testNamespace, types.UID("other-uid"), 0).
		WithEntries(testutils.NewPodGangEntryBuilder("pg-0", testHash, "e0").WithEpochAnchor(true).Build()).Build()
	r := newResource(newFakeClient(pcs, owned, other))

	require.NoError(t, r.Delete(context.Background(), logr.Discard(), pcs.ObjectMeta))

	assert.False(t, pgmExists(r.client, 0), "owned PodGangMap deleted")
	otherKey := client.ObjectKey{Namespace: testNamespace, Name: "other-pcs-0"}
	require.NoError(t, r.client.Get(context.Background(), otherKey, &grovecorev1alpha1.PodGangMap{}), "different-PCS PodGangMap untouched")
}

func TestBuildResource(t *testing.T) {
	pcs := pcsWithCoherentUpdateStrategy(testHash).Build()
	r := _resource{scheme: groveclientscheme.Scheme}
	pgm := &grovecorev1alpha1.PodGangMap{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pcs-0", Namespace: testNamespace},
	}
	entries := []grovecorev1alpha1.PodGangEntry{
		testutils.NewPodGangEntryBuilder("pg-c", testHash, "e2").Build(),
		testutils.NewPodGangEntryBuilder("pg-a", testHash, "e0").Build(),
		testutils.NewPodGangEntryBuilder("pg-b", testHash, "e1").Build(),
	}

	require.NoError(t, r.buildResource(pgm, pcs, 0, entries))

	assert.True(t, metav1.IsControlledBy(pgm, pcs))
	assert.Equal(t, int32(0), pgm.Spec.PodCliqueSetReplicaIndex)
	assert.Equal(t, getLabels(testPCSName, 0), pgm.Labels)
	names := make([]string, len(pgm.Spec.Entries))
	for i, e := range pgm.Spec.Entries {
		names[i] = e.Name
	}
	assert.Equal(t, []string{"pg-a", "pg-b", "pg-c"}, names, "entries sorted by name")
}
