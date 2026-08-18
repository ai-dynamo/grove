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

package podgangmigrator

import (
	"context"
	"testing"

	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const testNamespace = "default"

func TestSetMigrationGateForLegacyPodCliqueSets(t *testing.T) {
	t.Run("sets the gate on a legacy PodCliqueSet with no PodGangMap", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder("legacy", testNamespace, "uid").WithReplicas(1).Build()
		cl := newClientWithPCSStatus(pcs)

		err := SetMigrationGateForLegacyPodCliqueSets(context.Background(), cl, logr.Discard())
		require.NoError(t, err)
		assert.True(t, isGateSet(t, cl, "legacy"))
	})

	t.Run("skips a zero-replica PodCliqueSet", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder("zero", testNamespace, "uid").WithReplicas(0).Build()
		cl := newClientWithPCSStatus(pcs)

		err := SetMigrationGateForLegacyPodCliqueSets(context.Background(), cl, logr.Discard())
		require.NoError(t, err)
		assert.False(t, isGateSet(t, cl, "zero"))
	})

	t.Run("skips a PodCliqueSet whose gate is already set", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder("already", testNamespace, "uid").WithReplicas(1).
			WithStatusConditions(metav1.Condition{
				Type:   apiconstants.ConditionTypePodGangMigrationInProgress,
				Status: metav1.ConditionTrue,
				Reason: "PreSet",
			}).Build()
		cl := newClientWithPCSStatus(pcs)

		err := SetMigrationGateForLegacyPodCliqueSets(context.Background(), cl, logr.Discard())
		require.NoError(t, err)
		assert.True(t, isGateSet(t, cl, "already"))
	})

	t.Run("skips an already-migrated PodCliqueSet that has a PodGangMap", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder("migrated", testNamespace, "uid").WithReplicas(1).Build()
		pgm := testutils.NewPodGangMapBuilder("migrated", testNamespace, types.UID("uid"), 0).Build()
		cl := newClientWithPCSStatus(pcs, pgm)

		err := SetMigrationGateForLegacyPodCliqueSets(context.Background(), cl, logr.Discard())
		require.NoError(t, err)
		assert.False(t, isGateSet(t, cl, "migrated"))
	})
}

func TestIsMigrationInProgress(t *testing.T) {
	t.Run("true when the condition is True", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder("pcs", testNamespace, "uid").
			WithStatusConditions(metav1.Condition{
				Type:   apiconstants.ConditionTypePodGangMigrationInProgress,
				Status: metav1.ConditionTrue,
				Reason: "Pending",
			}).Build()
		assert.True(t, IsMigrationInProgress(pcs))
	})

	t.Run("false when the condition is absent", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder("pcs", testNamespace, "uid").Build()
		assert.False(t, IsMigrationInProgress(pcs))
	})
}

func newClientWithPCSStatus(objs ...client.Object) client.Client {
	return testutils.NewTestClientBuilder().
		WithObjects(objs...).
		WithStatusSubresource(&grovecorev1alpha1.PodCliqueSet{}).
		Build()
}

func isGateSet(t *testing.T, cl client.Client, name string) bool {
	t.Helper()
	pcs := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: name}, pcs))
	return meta.IsStatusConditionTrue(pcs.Status.Conditions, apiconstants.ConditionTypePodGangMigrationInProgress)
}
