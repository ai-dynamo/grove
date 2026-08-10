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

package validation

import (
	"context"
	"strings"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	groveconfigv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/resourceclaim"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
)

func TestGeneratedResourceClaimNameChecksUseRuntimeGenerators(t *testing.T) {
	fldPath := field.NewPath("spec", "template")
	pcs := createTestPodCliqueSet("root")
	pcs.Spec.Replicas = 2
	pcs.Spec.Template.ResourceSharing = []grovecorev1alpha1.PCSResourceSharingSpec{
		{ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{
			Name:  "pcs-all",
			Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas,
		}},
		{ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{
			Name:  "pcs-per",
			Scope: grovecorev1alpha1.ResourceSharingScopePerReplica,
		}},
	}

	standalone := pcs.Spec.Template.Cliques[0]
	standalone.Name = "solo"
	standalone.Spec.Replicas = 4
	standalone.ResourceSharing = []grovecorev1alpha1.ResourceSharingSpec{
		{Name: "standalone-all", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
		{Name: "standalone-per", Scope: grovecorev1alpha1.ResourceSharingScopePerReplica},
	}

	grouped := createDummyPodCliqueTemplate("worker")
	grouped.Spec.Replicas = 5
	grouped.ResourceSharing = []grovecorev1alpha1.ResourceSharingSpec{
		{Name: "grouped-all", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
		{Name: "grouped-per", Scope: grovecorev1alpha1.ResourceSharingScopePerReplica},
	}
	pcs.Spec.Template.Cliques = append(pcs.Spec.Template.Cliques, grouped)
	pcs.Spec.Template.PodCliqueScalingGroupConfigs = []grovecorev1alpha1.PodCliqueScalingGroupConfig{{
		Name:         "sg",
		CliqueNames:  []string{"worker"},
		Replicas:     ptr.To(int32(3)),
		MinAvailable: ptr.To(int32(1)),
		ResourceSharing: []grovecorev1alpha1.PCSGResourceSharingSpec{
			{ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{
				Name:  "pcsg-all",
				Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas,
			}},
			{ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{
				Name:  "pcsg-per",
				Scope: grovecorev1alpha1.ResourceSharingScopePerReplica,
			}},
		},
	}}

	checks := indexGeneratedResourceClaimNameChecksByIdentity(
		buildGeneratedResourceClaimNameChecks(pcs, fldPath),
	)
	require.Len(t, checks, 8)

	pcsgName := apicommon.GeneratePodCliqueScalingGroupName(
		apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 1},
		"sg",
	)
	standalonePCLQName := apicommon.GeneratePodCliqueName(
		apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 1},
		"solo",
	)
	groupedPCLQName := apicommon.GeneratePodCliqueName(
		apicommon.ResourceNameReplica{Name: pcsgName, Replica: 2},
		"worker",
	)

	tests := []struct {
		identity      string
		generatedName string
		replicaLimits []int32
	}{
		{"pcs/pcs-all/AllReplicas", resourceclaim.AllReplicasRCName(pcs.Name, "pcs-all"), nil},
		{"pcs/pcs-per/PerReplica", resourceclaim.PerReplicaRCName(pcs.Name, 1, "pcs-per"), []int32{2}},
		{"pcsg/sg/pcsg-all/AllReplicas", resourceclaim.AllReplicasRCName(pcsgName, "pcsg-all"), []int32{2}},
		{"pcsg/sg/pcsg-per/PerReplica", resourceclaim.PerReplicaRCName(pcsgName, 2, "pcsg-per"), []int32{2, 3}},
		{"pclq/solo/standalone/standalone-all/AllReplicas", resourceclaim.AllReplicasRCName(standalonePCLQName, "standalone-all"), []int32{2}},
		{"pclq/solo/standalone/standalone-per/PerReplica", resourceclaim.PerReplicaRCName(standalonePCLQName, 3, "standalone-per"), []int32{2, 4}},
		{"pclq/worker/pcsg/sg/grouped-all/AllReplicas", resourceclaim.AllReplicasRCName(groupedPCLQName, "grouped-all"), []int32{2, 3}},
		{"pclq/worker/pcsg/sg/grouped-per/PerReplica", resourceclaim.PerReplicaRCName(groupedPCLQName, 4, "grouped-per"), []int32{2, 3, 5}},
	}
	for _, tc := range tests {
		t.Run(tc.identity, func(t *testing.T) {
			check, exists := checks[tc.identity]
			require.True(t, exists)
			assert.Equal(t, tc.generatedName, check.maxGeneratedName)
			assert.Equal(t, tc.replicaLimits, check.replicaLimits)
		})
	}
}

func TestValidateGeneratedResourceClaimNames(t *testing.T) {
	fldPath := field.NewPath("spec", "template")
	tests := []struct {
		name           string
		refName        string
		expectedDetail string
	}{
		{
			name:    "63-character generated name is valid",
			refName: strings.Repeat("a", 57),
		},
		{
			name:           "64-character generated name is rejected",
			refName:        strings.Repeat("a", 58),
			expectedDetail: "must be no more than 63 characters",
		},
		{
			name:           "short generated name with a dot is rejected",
			refName:        "shared.gpu",
			expectedDetail: "must not contain dots",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := createTestPodCliqueSet("p")
			pcs.Spec.Template.ResourceSharing = []grovecorev1alpha1.PCSResourceSharingSpec{{
				ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{
					Name:  tc.refName,
					Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas,
				},
			}}

			errs := newGeneratedResourceClaimNameValidator(pcs).validate(fldPath)
			if tc.expectedDetail == "" {
				assert.Empty(t, errs)
				return
			}
			require.Len(t, errs, 1)
			assert.Equal(t, "spec.template.resourceSharing[0].name", errs[0].Field)
			assert.Equal(t, field.ErrorTypeInvalid, errs[0].Type)
			assert.Equal(t, tc.refName, errs[0].BadValue)
			assert.Contains(t, errs[0].Detail, "pod.spec.resourceClaims[].name")
			assert.Contains(t, errs[0].Detail, tc.expectedDetail)
		})
	}
}

func TestValidateGeneratedResourceClaimNamesRejectsOverlongPCLQScopedName(t *testing.T) {
	fldPath := field.NewPath("spec", "template")
	pcs := createTestPodCliqueSet(strings.Repeat("a", 40))
	clique := pcs.Spec.Template.Cliques[0]
	clique.Name = "workr"
	clique.ResourceSharing = []grovecorev1alpha1.ResourceSharingSpec{{
		Name:  "shared-gpus",
		Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas,
	}}

	errs := newGeneratedResourceClaimNameValidator(pcs).validate(fldPath)
	require.Len(t, errs, 1)
	assert.Equal(t, "spec.template.cliques[0].resourceSharing[0].name", errs[0].Field)
	assert.Equal(t, "shared-gpus", errs[0].BadValue)
	assert.Contains(t, errs[0].Detail, strings.Repeat("a", 40)+"-0-workr-all-shared-gpus")
}

func TestValidateGeneratedResourceClaimNamesUsesPCLQAutoscalingMaximum(t *testing.T) {
	fldPath := field.NewPath("spec", "template")
	pcs := createTestPodCliqueSet(strings.Repeat("p", 30))
	clique := pcs.Spec.Template.Cliques[0]
	clique.Name = strings.Repeat("c", 10)
	clique.ResourceSharing = []grovecorev1alpha1.ResourceSharingSpec{{
		Name:  strings.Repeat("r", 17),
		Scope: grovecorev1alpha1.ResourceSharingScopePerReplica,
	}}
	clique.Spec.ScaleConfig = &grovecorev1alpha1.AutoScalingConfig{
		MinReplicas: ptr.To(int32(1)),
		MaxReplicas: 10,
	}

	validator := newGeneratedResourceClaimNameValidator(pcs)
	assert.Empty(t, validator.validate(fldPath))

	clique.Spec.ScaleConfig.MaxReplicas = 11
	errs := validator.validate(fldPath)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Detail, "-10-")
}

func TestValidateGeneratedResourceClaimNamesUsesPCSGAutoscalingMaximum(t *testing.T) {
	fldPath := field.NewPath("spec", "template")
	pcs := createTestPodCliqueSet(strings.Repeat("p", 30))
	pcs.Spec.Template.PodCliqueScalingGroupConfigs = []grovecorev1alpha1.PodCliqueScalingGroupConfig{{
		Name:         strings.Repeat("g", 10),
		CliqueNames:  []string{pcs.Spec.Template.Cliques[0].Name},
		Replicas:     ptr.To(int32(1)),
		MinAvailable: ptr.To(int32(1)),
		ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{
			MinReplicas: ptr.To(int32(1)),
			MaxReplicas: 10,
		},
		ResourceSharing: []grovecorev1alpha1.PCSGResourceSharingSpec{{
			ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{
				Name:  strings.Repeat("r", 17),
				Scope: grovecorev1alpha1.ResourceSharingScopePerReplica,
			},
		}},
	}}

	validator := newGeneratedResourceClaimNameValidator(pcs)
	assert.Empty(t, validator.validate(fldPath))

	pcs.Spec.Template.PodCliqueScalingGroupConfigs[0].ScaleConfig.MaxReplicas = 11
	errs := validator.validate(fldPath)
	require.Len(t, errs, 1)
	assert.Equal(t, "spec.template.podCliqueScalingGroups[0].resourceSharing[0].name", errs[0].Field)
	assert.Contains(t, errs[0].Detail, "-10-")
}

func TestValidateGeneratedResourceClaimNamesOnUpdate(t *testing.T) {
	fldPath := field.NewPath("spec", "template")
	validateUpdate := func(oldPCS, newPCS *grovecorev1alpha1.PodCliqueSet) field.ErrorList {
		return newGeneratedResourceClaimNameValidator(newPCS).validateUpdate(oldPCS, fldPath)
	}
	oldPCS := createTestPodCliqueSet("p")
	oldPCS.Spec.Template.ResourceSharing = []grovecorev1alpha1.PCSResourceSharingSpec{{
		ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{
			Name:  strings.Repeat("r", 58),
			Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas,
		},
	}}

	t.Run("unchanged legacy violation is allowed", func(t *testing.T) {
		newPCS := oldPCS.DeepCopy()
		assert.Empty(t, validateUpdate(oldPCS, newPCS))
	})

	t.Run("all-replicas violation allows scale out", func(t *testing.T) {
		newPCS := oldPCS.DeepCopy()
		newPCS.Spec.Replicas++
		assert.Empty(t, validateUpdate(oldPCS, newPCS))
	})

	t.Run("per-replica violation allows scale in", func(t *testing.T) {
		perReplicaOldPCS := oldPCS.DeepCopy()
		perReplicaOldPCS.Spec.Replicas = 2
		perReplicaOldPCS.Spec.Template.ResourceSharing[0].Scope = grovecorev1alpha1.ResourceSharingScopePerReplica
		perReplicaOldPCS.Spec.Template.ResourceSharing[0].Name = strings.Repeat("r", 60)
		newPCS := perReplicaOldPCS.DeepCopy()
		newPCS.Spec.Replicas--

		assert.Empty(t, validateUpdate(perReplicaOldPCS, newPCS))
	})

	t.Run("per-replica violation rejects scale out", func(t *testing.T) {
		perReplicaOldPCS := oldPCS.DeepCopy()
		perReplicaOldPCS.Spec.Template.ResourceSharing[0].Scope = grovecorev1alpha1.ResourceSharingScopePerReplica
		perReplicaOldPCS.Spec.Template.ResourceSharing[0].Name = strings.Repeat("r", 60)
		newPCS := perReplicaOldPCS.DeepCopy()
		newPCS.Spec.Replicas++

		errs := validateUpdate(perReplicaOldPCS, newPCS)
		require.Len(t, errs, 1)
		assert.Equal(t, "spec.template.resourceSharing[0].name", errs[0].Field)
	})

	t.Run("per-replica violation rejects autoscaling maximum increase", func(t *testing.T) {
		scaledOldPCS := createTestPodCliqueSet(strings.Repeat("p", 30))
		clique := scaledOldPCS.Spec.Template.Cliques[0]
		clique.Name = strings.Repeat("c", 10)
		clique.ResourceSharing = []grovecorev1alpha1.ResourceSharingSpec{{
			Name:  strings.Repeat("r", 18),
			Scope: grovecorev1alpha1.ResourceSharingScopePerReplica,
		}}
		clique.Spec.ScaleConfig = &grovecorev1alpha1.AutoScalingConfig{
			MinReplicas: ptr.To(int32(1)),
			MaxReplicas: 10,
		}
		newPCS := scaledOldPCS.DeepCopy()
		newPCS.Spec.Template.Cliques[0].Spec.ScaleConfig.MaxReplicas = 11

		errs := validateUpdate(scaledOldPCS, newPCS)
		require.Len(t, errs, 1)
		assert.Equal(t, "spec.template.cliques[0].resourceSharing[0].name", errs[0].Field)
	})

	t.Run("grouped PodClique violation rejects PCSG autoscaling maximum increase", func(t *testing.T) {
		groupedOldPCS := createPCSWithOverlongGroupedPCLQClaimName(1, 10)
		newPCS := groupedOldPCS.DeepCopy()
		newPCS.Spec.Template.PodCliqueScalingGroupConfigs[0].ScaleConfig.MaxReplicas = 11

		errs := validateUpdate(groupedOldPCS, newPCS)
		require.Len(t, errs, 1)
		assert.Equal(t, "spec.template.cliques[0].resourceSharing[0].name", errs[0].Field)
	})

	t.Run("grouped PodClique violation rejects PCS replica increase", func(t *testing.T) {
		groupedOldPCS := createPCSWithOverlongGroupedPCLQClaimName(10, 1)
		newPCS := groupedOldPCS.DeepCopy()
		newPCS.Spec.Replicas = 11

		errs := validateUpdate(groupedOldPCS, newPCS)
		require.Len(t, errs, 1)
		assert.Equal(t, "spec.template.cliques[0].resourceSharing[0].name", errs[0].Field)
	})
}

func TestHandlerValidatesGeneratedResourceClaimNames(t *testing.T) {
	handler := newGeneratedNameTestHandler()
	pcs := createTestPodCliqueSet("p")
	pcs.Spec.Template.ResourceSharing = []grovecorev1alpha1.PCSResourceSharingSpec{{
		ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{
			Name:  strings.Repeat("r", 58),
			Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas,
		},
	}}

	_, err := handler.ValidateCreate(context.Background(), pcs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pod.spec.resourceClaims[].name")

	_, err = handler.ValidateUpdate(context.Background(), pcs, pcs.DeepCopy())
	require.NoError(t, err)
}

func newGeneratedNameTestHandler() *Handler {
	cl := testutils.NewTestClientBuilder().Build()
	mgr := &testutils.FakeManager{
		Client: cl,
		Scheme: cl.Scheme(),
		Logger: logr.Discard(),
	}
	cfg := groveconfigv1alpha1.OperatorConfiguration{
		TopologyAwareScheduling: getDefaultTASConfig(),
		Network:                 getDefaultNetworkConfig(),
		Scheduler: groveconfigv1alpha1.SchedulerConfiguration{
			Profiles:           []groveconfigv1alpha1.SchedulerProfile{{Name: groveconfigv1alpha1.SchedulerNameKube}},
			DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube),
		},
	}
	return NewHandler(mgr, &cfg, testutils.NewDefaultFakeRegistry())
}

func createPCSWithOverlongGroupedPCLQClaimName(
	pcsReplicas, pcsgMaxReplicas int32,
) *grovecorev1alpha1.PodCliqueSet {
	pcs := createTestPodCliqueSet(strings.Repeat("p", 30))
	pcs.Spec.Replicas = pcsReplicas
	clique := pcs.Spec.Template.Cliques[0]
	clique.Name = strings.Repeat("c", 10)
	clique.ResourceSharing = []grovecorev1alpha1.ResourceSharingSpec{{
		Name:  "gpu",
		Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas,
	}}
	pcs.Spec.Template.PodCliqueScalingGroupConfigs = []grovecorev1alpha1.PodCliqueScalingGroupConfig{{
		Name:         strings.Repeat("g", 10),
		CliqueNames:  []string{clique.Name},
		Replicas:     ptr.To(int32(1)),
		MinAvailable: ptr.To(int32(1)),
		ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{
			MinReplicas: ptr.To(int32(1)),
			MaxReplicas: pcsgMaxReplicas,
		},
	}}
	return pcs
}
