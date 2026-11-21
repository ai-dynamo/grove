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

package scalein

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func podWithDeletionCost(name, value string) *corev1.Pod {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if value != "" {
		pod.Annotations = map[string]string{corev1.PodDeletionCost: value}
	}
	return pod
}

func TestPodDeletionCostSourceCostOf(t *testing.T) {
	source, err := NewCostSource(configv1beta1.ScaleInCostSourcePodDeletionCost)
	require.NoError(t, err)

	tests := []struct {
		name        string
		value       string
		wantCost    int32
		wantPresent bool
	}{
		{name: "present", value: "42", wantCost: 42, wantPresent: true},
		{name: "negative", value: "-1000000", wantCost: -1000000, wantPresent: true},
		{name: "zero is present", value: "0", wantCost: 0, wantPresent: true},
		{name: "absent", value: "", wantCost: 0, wantPresent: false},
		{name: "malformed is absent", value: "lots", wantCost: 0, wantPresent: false},
		{name: "out of int32 range is absent", value: "4294967296", wantCost: 0, wantPresent: false},
		{name: "a leading plus sign is absent, as for a ReplicaSet", value: "+5", wantCost: 0, wantPresent: false},
		{name: "a leading zero is absent, as for a ReplicaSet", value: "007", wantCost: 0, wantPresent: false},
		{name: "a negative zero is absent", value: "-0", wantCost: 0, wantPresent: true},
		{name: "empty string is absent", value: " ", wantCost: 0, wantPresent: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cost, present := source.CostOf(podWithDeletionCost("pod", test.value))
			assert.Equal(t, test.wantCost, cost)
			assert.Equal(t, test.wantPresent, present)
		})
	}
}

func TestNoCostSourceReportsEveryPodAbsent(t *testing.T) {
	for _, name := range []configv1beta1.ScaleInCostSourceName{"", configv1beta1.ScaleInCostSourceNone} {
		source, err := NewCostSource(name)
		require.NoError(t, err)
		cost, present := source.CostOf(podWithDeletionCost("pod", "-1000000"))
		assert.Zero(t, cost)
		assert.False(t, present)
	}
}

func TestNewCostSourceRejectsUnknownName(t *testing.T) {
	source, err := NewCostSource("grove-annotation")
	require.Error(t, err)
	assert.Nil(t, source)
	assert.Contains(t, err.Error(), "grove-annotation")
}

func TestCostOfTreatsNilSourceAsNoCost(t *testing.T) {
	assert.Zero(t, CostOf(nil, podWithDeletionCost("pod", "-5")))
}

func TestReadsNoCost(t *testing.T) {
	noSource, err := NewCostSource(configv1beta1.ScaleInCostSourceNone)
	require.NoError(t, err)
	podDeletionCost, err := NewCostSource(configv1beta1.ScaleInCostSourcePodDeletionCost)
	require.NoError(t, err)

	assert.True(t, ReadsNoCost(nil))
	assert.True(t, ReadsNoCost(noSource))
	assert.False(t, ReadsNoCost(podDeletionCost))
}

func TestMinCost(t *testing.T) {
	source, err := NewCostSource(configv1beta1.ScaleInCostSourcePodDeletionCost)
	require.NoError(t, err)

	tests := []struct {
		name   string
		values []string
		want   int32
	}{
		{name: "no pods", want: 0},
		{name: "no pod marked", values: []string{"", ""}, want: 0},
		{name: "one marked pod speaks for the gang", values: []string{"", "-1000000", ""}, want: -1000000},
		{name: "an unmarked pod costs 0 and caps a positive mark", values: []string{"7", ""}, want: 0},
		{name: "every pod marked takes the minimum", values: []string{"7", "3", "9"}, want: 3},
		{name: "a malformed mark counts as 0", values: []string{"garbage", "4"}, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pods := make([]*corev1.Pod, 0, len(test.values))
			for i, value := range test.values {
				pods = append(pods, podWithDeletionCost(strings.Repeat("p", i+1), value))
			}
			assert.Equal(t, test.want, MinCost(source, pods))
		})
	}
}

// TestCarrierIsKnownInOnePlace pins the layering: the annotation that carries a pod's scale-in cost is read in
// this package's source and nowhere else in the operator, so selection code consumes a cost and never a key.
// The key is corev1.PodDeletionCost; a source file that names that constant, or spells the key out as a string
// literal, reads the carrier. Prose that mentions the key, such as the configuration API's documentation, does not.
func TestCarrierIsKnownInOnePlace(t *testing.T) {
	readsCarrier := regexp.MustCompile(`\bPodDeletionCost\b|"controller\.kubernetes\.io/pod-deletion-cost"`)
	operatorRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	ownSource, err := filepath.Abs("costsource.go")
	require.NoError(t, err)

	var readers []string
	err = filepath.WalkDir(operatorRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Vendored modules and the e2e suite are not operator source.
			if name := d.Name(); name == "vendor" || name == "e2e" || name == "hack" || strings.HasPrefix(name, ".") && path != operatorRoot {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if readsCarrier.Match(content) {
			readers = append(readers, path)
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{ownSource}, readers, "the pod-deletion-cost carrier must be read by the cost source only")
}
