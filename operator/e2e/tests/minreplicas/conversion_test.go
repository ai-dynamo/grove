//go:build e2e

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

package minreplicas

import (
	"testing"
	"time"

	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

// Test_MRM19_V1Alpha1RoundTripKeepsV1Beta1Values verifies that a v1alpha1 update of a PodCliqueSet keeps the
// v1beta1 values that v1alpha1 cannot express, and that status.gangShapeRecreate is visible through v1alpha1.
func Test_MRM19_V1Alpha1RoundTripKeepsV1Beta1Values(t *testing.T) {
	const pcsName = "mrm19"
	tc, cleanup := prepare(t, 10, pcsName)
	defer cleanup()
	pcs := newPCS(pcsName, 2, grovev1beta1.CoherentStrategy, []cliqueSpec{
		{name: workerClique, replicas: 3, init: 3, availability: 2, breachTolerance: 45 * time.Second},
	})
	cliqueTemplate(pcs, workerClique).RollingUpdate = &grovev1beta1.RollingUpdateConfiguration{
		MinReplicas:    ptr.To[int32](2),
		MaxUnavailable: ptr.To(intstr.FromInt32(2)),
	}
	deploy(t, tc, pcs)

	tests.Logger.Info("1. The v1alpha1 view shows minAvailable 3 and stashes the values it cannot express")
	alpha := getAlphaPCS(t, tc, pcsName)
	require.Contains(t, alpha.GetAnnotations(), grovev1alpha1.ConversionDataAnnotation)
	cliques, _, err := unstructured.NestedSlice(alpha.Object, "spec", "template", "cliques")
	require.NoError(t, err)
	minAvailable, _, err := unstructured.NestedInt64(cliques[0].(map[string]any), "spec", "minAvailable")
	require.NoError(t, err)
	require.Equal(t, int64(3), minAvailable)

	tests.Logger.Info("2. A v1alpha1 update keeps every v1beta1 value")
	require.NoError(t, updateAlphaPCS(tc, pcsName, setAlphaAnnotation("e2e.grove.io/touched", "true")))
	beta := getPCS(t, tc, pcsName)
	require.NotContains(t, beta.Annotations, grovev1alpha1.ConversionDataAnnotation, "the stash must not show in v1beta1")
	worker := cliqueTemplate(beta, workerClique)
	availability := componentutils.Availability(worker.Spec.Termination)
	require.Equal(t, int32(3), *worker.Spec.Init.MinReplicas)
	require.Equal(t, int32(2), *availability.MinReplicas)
	require.Equal(t, 45*time.Second, availability.BreachTolerance.Duration)
	require.Equal(t, int32(2), *worker.RollingUpdate.MinReplicas)
	require.Equal(t, intstr.FromInt32(2), *worker.RollingUpdate.MaxUnavailable)

	tests.Logger.Info("3. status.gangShapeRecreate of an in-progress recreate is visible through v1alpha1")
	hold := holdStage(pcsName, 0)
	hold.apply(t, tc)
	requireAccepted(t, tc, pcsName, "init.minReplicas 2", setWorkerInit(2))
	beta = waitForPCS(t, tc, pcsName, "replica 0 to be recreated", func(pcs *grovev1beta1.PodCliqueSet) bool {
		return pcs.Status.GangShapeRecreate != nil && pcs.Status.GangShapeRecreate.CurrentlyRecreating != nil
	})
	alphaProgress, found, err := unstructured.NestedMap(getAlphaPCS(t, tc, pcsName).Object, "status", "gangShapeRecreate")
	require.NoError(t, err)
	require.True(t, found, "v1alpha1 must show status.gangShapeRecreate")
	require.Equal(t, beta.Status.GangShapeRecreate.GangShapeHash, alphaProgress["gangShapeHash"])
	require.Equal(t, int64(*beta.Status.GangShapeRecreate.CurrentlyRecreating), alphaProgress["currentlyRecreating"])
	require.Equal(t, beta.Status.GangShapeRecreate.StartedAt.UTC().Format(time.RFC3339), alphaProgress["startedAt"])

	hold.release(t, tc)
	waitForGangShapeCurrent(t, tc, pcsName)
}

// Test_MRM20_V1Alpha1CreatedPodCliqueSet verifies that a PodCliqueSet created through v1alpha1 gets v1beta1 values
// derived from minAvailable and terminationDelay, that a v1alpha1 minAvailable change is rejected, and that a
// v1alpha1 terminationDelay change reaches breachTolerance.
func Test_MRM20_V1Alpha1CreatedPodCliqueSet(t *testing.T) {
	const pcsName = "mrm20"
	tc, cleanup := prepare(t, 6, pcsName)
	defer cleanup()

	tests.Logger.Info("1. Create the PodCliqueSet through v1alpha1 with minAvailable 2 and terminationDelay 45s")
	require.NoError(t, tc.Client.Create(tc.Ctx, newAlphaPCS(t, pcsName, 3, 2, "45s")))
	waitForReadyPods(t, tc, pcsName, 3)

	tests.Logger.Info("2. With nothing to stash, the v1beta1 values are derived from v1alpha1")
	require.NotContains(t, getAlphaPCS(t, tc, pcsName).GetAnnotations(), grovev1alpha1.ConversionDataAnnotation)
	worker := cliqueTemplate(getPCS(t, tc, pcsName), workerClique)
	availability := componentutils.Availability(worker.Spec.Termination)
	require.Equal(t, int32(2), *worker.Spec.Init.MinReplicas)
	require.Equal(t, int32(2), *availability.MinReplicas)
	require.Equal(t, 45*time.Second, availability.BreachTolerance.Duration)

	tests.Logger.Info("3. A v1alpha1 minAvailable change is rejected")
	requireRejectedError(t, updateAlphaPCS(tc, pcsName, setAlphaCliqueMinAvailable(workerClique, 3)), "a v1alpha1 minAvailable change",
		"field is immutable for grove.io/v1alpha1 requests, use grove.io/v1beta1 to change init.minReplicas")

	tests.Logger.Info("4. A v1alpha1 terminationDelay change reaches breachTolerance")
	require.NoError(t, updateAlphaPCS(tc, pcsName, func(obj *unstructured.Unstructured) error {
		return unstructured.SetNestedField(obj.Object, "1m0s", "spec", "template", "terminationDelay")
	}))
	availability = componentutils.Availability(cliqueTemplate(getPCS(t, tc, pcsName), workerClique).Spec.Termination)
	require.Equal(t, time.Minute, availability.BreachTolerance.Duration)
	require.Equal(t, int32(2), *availability.MinReplicas)
}

// newAlphaPCS returns a grove.io/v1alpha1 PodCliqueSet with one replica and a worker clique of cliqueReplicas pods.
func newAlphaPCS(t *testing.T, name string, cliqueReplicas, minAvailable int64, terminationDelay string) *unstructured.Unstructured {
	t.Helper()
	podSpec, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ptr.To(newPodSpec(workerClique)))
	require.NoError(t, err)
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": name, "namespace": testNamespace},
		"spec": map[string]any{
			"replicas":       int64(1),
			"updateStrategy": map[string]any{"type": string(grovev1beta1.RollingRecreateStrategy)},
			"template": map[string]any{
				"terminationDelay": terminationDelay,
				"cliques": []any{map[string]any{
					"name":   workerClique,
					"labels": map[string]any{"kai.scheduler/queue": "test"},
					"spec": map[string]any{
						"roleName":     workerClique,
						"replicas":     cliqueReplicas,
						"minAvailable": minAvailable,
						"podSpec":      podSpec,
					},
				}},
			},
		},
	}}
	obj.SetGroupVersionKind(alphaPCSGVK)
	return obj
}
