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

package component

import (
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

func TestPodCliqueByName(t *testing.T) {
	testCases := []struct {
		description string
		pclqs       []grovecorev1alpha1.PodClique
		wantNames   []string
	}{
		{description: "nil slice yields empty map"},
		{
			description: "maps each PodClique by its name",
			pclqs: []grovecorev1alpha1.PodClique{
				{ObjectMeta: metav1.ObjectMeta{Name: "prefill"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "decode"}},
			},
			wantNames: []string{"prefill", "decode"},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			got := PodCliqueByName(tc.pclqs)
			assert.Len(t, got, len(tc.wantNames))
			for _, name := range tc.wantNames {
				pclq, ok := got[name]
				assert.True(t, ok)
				assert.Equal(t, name, pclq.Name)
			}
		})
	}
}

func TestPodCliqueNameSet(t *testing.T) {
	testCases := []struct {
		description string
		pclqs       []grovecorev1alpha1.PodClique
		wantNames   []string
	}{
		{description: "nil slice yields empty set"},
		{
			description: "collects each PodClique name",
			pclqs: []grovecorev1alpha1.PodClique{
				{ObjectMeta: metav1.ObjectMeta{Name: "prefill"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "decode"}},
			},
			wantNames: []string{"prefill", "decode"},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			assert.Equal(t, sets.New(tc.wantNames...), PodCliqueNameSet(tc.pclqs))
		})
	}
}

func TestPodGangByName(t *testing.T) {
	testCases := []struct {
		description string
		podGangs    []groveschedulerv1alpha1.PodGang
		wantNames   []string
	}{
		{description: "nil slice yields empty map"},
		{
			description: "maps each PodGang by its name",
			podGangs: []groveschedulerv1alpha1.PodGang{
				{ObjectMeta: metav1.ObjectMeta{Name: "pg-0"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "pg-1"}},
			},
			wantNames: []string{"pg-0", "pg-1"},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			got := PodGangByName(tc.podGangs)
			assert.Len(t, got, len(tc.wantNames))
			for _, name := range tc.wantNames {
				podGang, ok := got[name]
				assert.True(t, ok)
				assert.Equal(t, name, podGang.Name)
			}
		})
	}
}
