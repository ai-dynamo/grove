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

	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

const (
	memberClique = "member"
	scalingGroup = "sg"
)

// newAdmissionPCS returns a PodCliqueSet with a standalone clique (replicas 3, init 2, availability 1), and a
// scaling group (replicas 2, init 2, availability 1) whose member clique has no availability. It runs 5 pods.
func newAdmissionPCS(name string) *grovev1beta1.PodCliqueSet {
	return newPCS(name, 1, grovev1beta1.RollingRecreateStrategy,
		[]cliqueSpec{
			{name: workerClique, replicas: 3, init: 2, availability: 1},
			{name: memberClique, replicas: 1, init: 1},
		},
		scalingGroupSpec{name: scalingGroup, cliqueNames: []string{memberClique}, replicas: 2, init: 2, availability: 1},
	)
}

// Test_MRM1_MinReplicasOrderingOnUpdate verifies that an update must keep availability.minReplicas <= init.minReplicas
// <= replicas, for a standalone clique and for a scaling group.
func Test_MRM1_MinReplicasOrderingOnUpdate(t *testing.T) {
	const pcsName = "mrm1"
	tc, cleanup := prepare(t, 8, pcsName)
	defer cleanup()
	deploy(t, tc, newAdmissionPCS(pcsName))

	tests.Logger.Info("1. Standalone clique: init above replicas, availability above init, and init below availability are rejected")
	requireRejected(t, tc, pcsName, "init.minReplicas 4 with replicas 3", "must not be greater than replicas (3)", setWorkerInit(4))
	requireRejected(t, tc, pcsName, "availability.minReplicas 3 with init 2", "must not be greater than init.minReplicas (2)", setCliqueAvailability(workerClique, 3))
	requireAccepted(t, tc, pcsName, "availability.minReplicas 2 with init 2", setCliqueAvailability(workerClique, 2))
	requireRejected(t, tc, pcsName, "init.minReplicas 1 with availability 2", "must not be greater than init.minReplicas (1)", setWorkerInit(1))

	tests.Logger.Info("2. Scaling group: the same ordering applies")
	requireRejected(t, tc, pcsName, "scaling group init.minReplicas 3 with replicas 2", "must not be greater than replicas (2)", setGroupInit(scalingGroup, 3))
	requireRejected(t, tc, pcsName, "scaling group availability.minReplicas 3 with init 2", "must not be greater than init.minReplicas (2)", setGroupAvailability(scalingGroup, 3))
	requireAccepted(t, tc, pcsName, "scaling group availability.minReplicas 2 with init 2", setGroupAvailability(scalingGroup, 2))
	requireRejected(t, tc, pcsName, "scaling group init.minReplicas 1 with availability 2", "must not be greater than init.minReplicas (1)", setGroupInit(scalingGroup, 1))

	tests.Logger.Info("3. A valid init.minReplicas change is accepted")
	requireAccepted(t, tc, pcsName, "init.minReplicas 3 with replicas 3 and availability 2", setWorkerInit(3))
	pcs := getPCS(t, tc, pcsName)
	require.Equal(t, int32(3), *cliqueTemplate(pcs, workerClique).Spec.Init.MinReplicas)
	require.Equal(t, int32(2), *componentutils.Availability(cliqueTemplate(pcs, workerClique).Spec.Termination).MinReplicas)
	require.Equal(t, int32(2), *componentutils.Availability(scalingGroupConfig(pcs, scalingGroup).Termination).MinReplicas)
}

// Test_MRM2_V1Alpha1RequestCannotChangeMinReplicas verifies that a v1alpha1 request cannot change the minimums
// through minAvailable, that it can still change something else without losing the v1beta1 values, and that a
// strategy switch that adds rollingUpdate.minReplicas is not counted as a change.
func Test_MRM2_V1Alpha1RequestCannotChangeMinReplicas(t *testing.T) {
	const pcsName = "mrm2"
	tc, cleanup := prepare(t, 8, pcsName)
	defer cleanup()
	deploy(t, tc, newAdmissionPCS(pcsName))
	const message = "field is immutable for grove.io/v1alpha1 requests, use grove.io/v1beta1 to change init.minReplicas"

	tests.Logger.Info("1. A v1alpha1 minAvailable change is rejected, for a clique and for a scaling group")
	requireRejectedError(t, updateAlphaPCS(tc, pcsName, setAlphaCliqueMinAvailable(workerClique, 3)), "a v1alpha1 clique minAvailable change", message)
	requireRejectedError(t, updateAlphaPCS(tc, pcsName, setAlphaGroupMinAvailable(scalingGroup, 1)), "a v1alpha1 scaling group minAvailable change", message)

	tests.Logger.Info("2. A v1alpha1 request that changes something else is accepted and keeps the v1beta1 values")
	require.NoError(t, updateAlphaPCS(tc, pcsName, setAlphaAnnotation("e2e.grove.io/touched", "true")), "an unrelated v1alpha1 change must be accepted")
	requireMinReplicas(t, getPCS(t, tc, pcsName), 2, 1, 2, 1)

	tests.Logger.Info("3. A v1alpha1 switch to Coherent, which adds rollingUpdate.minReplicas, is accepted")
	require.NoError(t, updateAlphaPCS(tc, pcsName,
		setAlphaStrategy(string(grovev1beta1.CoherentStrategy)),
		setAlphaCliqueMaxUnavailable(workerClique, 2),
		setAlphaGroupMaxUnavailable(scalingGroup, 2),
	), "a v1alpha1 strategy switch must be accepted")
	pcs := getPCS(t, tc, pcsName)
	require.Equal(t, grovev1beta1.CoherentStrategy, pcs.Spec.UpdateStrategy.Type)
	require.NotNil(t, cliqueTemplate(pcs, workerClique).RollingUpdate.MinReplicas)
	requireMinReplicas(t, pcs, 2, 1, 2, 1)
}

// requireMinReplicas checks init.minReplicas and availability.minReplicas of the worker clique and the scaling group.
func requireMinReplicas(t *testing.T, pcs *grovev1beta1.PodCliqueSet, workerInit, workerAvailability, groupInit, groupAvailability int32) {
	t.Helper()
	worker := cliqueTemplate(pcs, workerClique)
	group := scalingGroupConfig(pcs, scalingGroup)
	require.Equal(t, workerInit, *worker.Spec.Init.MinReplicas, "worker init.minReplicas")
	require.Equal(t, workerAvailability, *componentutils.Availability(worker.Spec.Termination).MinReplicas, "worker availability.minReplicas")
	require.Equal(t, groupInit, *group.Init.MinReplicas, "scaling group init.minReplicas")
	require.Equal(t, groupAvailability, *componentutils.Availability(group.Termination).MinReplicas, "scaling group availability.minReplicas")
}

// Test_MRM3_AddingOrRemovingAvailabilityIsRejected verifies that availability cannot be added or removed after
// creation, because that switches gang termination on or off.
func Test_MRM3_AddingOrRemovingAvailabilityIsRejected(t *testing.T) {
	const pcsName = "mrm3"
	tc, cleanup := prepare(t, 8, pcsName)
	defer cleanup()
	deploy(t, tc, newAdmissionPCS(pcsName))
	const message = "adding or removing availability is not allowed, because it switches gang termination on or off"

	requireRejected(t, tc, pcsName, "removing availability from a clique", message, setCliqueTermination(workerClique, nil))
	requireRejected(t, tc, pcsName, "removing availability from a scaling group", message, setGroupTermination(scalingGroup, nil))
	requireRejected(t, tc, pcsName, "adding availability to a clique", message, setCliqueTermination(memberClique, newTermination(1, 0)))
}

// Test_MRM4_AutoscalingFloorAndScaleGuardFollowAvailability verifies that a raised availability.minReplicas
// becomes the floor for autoScalingConfig.minReplicas and for the scale guard.
func Test_MRM4_AutoscalingFloorAndScaleGuardFollowAvailability(t *testing.T) {
	const pcsName = "mrm4"
	const otherClique = "other"
	tc, cleanup := prepare(t, 9, pcsName)
	defer cleanup()
	deploy(t, tc, newPCS(pcsName, 1, grovev1beta1.RollingRecreateStrategy, []cliqueSpec{
		{name: workerClique, replicas: 3, init: 2, availability: 1, autoScaleMax: 4},
		{name: otherClique, replicas: 3, init: 2, availability: 1},
	}))

	tests.Logger.Info("1. Raising availability above autoScalingConfig.minReplicas is rejected, raising both is accepted")
	requireRejected(t, tc, pcsName, "availability.minReplicas 2 with autoScalingConfig.minReplicas 1",
		"must be greater than or equal to 2 (termination.resource.availability.minReplicas", setCliqueAvailability(workerClique, 2))
	requireAccepted(t, tc, pcsName, "availability.minReplicas and autoScalingConfig.minReplicas 2",
		setCliqueAvailability(workerClique, 2), setAutoScaleMin(workerClique, 2))

	tests.Logger.Info("2. The scale guard uses the raised availability.minReplicas")
	requireAccepted(t, tc, pcsName, "availability.minReplicas 2 on a clique without autoscaling", setCliqueAvailability(otherClique, 2))
	otherPCLQ := pclqName(pcsName, 0, otherClique)
	waitForPCLQ(t, tc, otherPCLQ, "availability.minReplicas 2", func(pclq *grovev1beta1.PodClique) bool {
		return ptr.Deref(componentutils.Availability(pclq.Spec.Termination).MinReplicas, 0) == 2
	})
	err := tc.ScalePodClique(otherPCLQ, 1)
	requireRejectedError(t, err, "scaling to 1 below availability 2", "must be either 0 or at least termination.resource.availability.minReplicas 2")
	require.NoError(t, tc.ScalePodClique(otherPCLQ, 0), "scaling to 0 must be allowed")
}

// Test_MRM5_RollingUpdateMinReplicasBounds verifies the bounds of rollingUpdate.minReplicas and maxUnavailable
// under the Coherent and RollingRecreate strategies.
func Test_MRM5_RollingUpdateMinReplicasBounds(t *testing.T) {
	const coherentName = "mrm5-coherent"
	const rollingName = "mrm5-rolling"
	tc, cleanup := prepare(t, 9, coherentName)
	defer cleanup()
	deploy(t, tc, newPCS(coherentName, 1, grovev1beta1.CoherentStrategy, []cliqueSpec{{name: workerClique, replicas: 3, init: 2, availability: 1}}))
	deploy(t, tc, newPCS(rollingName, 1, grovev1beta1.RollingRecreateStrategy, []cliqueSpec{{name: workerClique, replicas: 3, init: 1, availability: 1}}))

	tests.Logger.Info("1. Coherent: rollingUpdate.minReplicas must not exceed min(init.minReplicas, replicas)")
	requireRejected(t, tc, coherentName, "rollingUpdate.minReplicas 3 with init 2", "must not be greater than min(init.minReplicas, replicas) (2)",
		setWorkerRollingUpdate(ptr.To[int32](3), ptr.To(intstr.FromInt32(3))))

	tests.Logger.Info("2. Coherent: maxUnavailable, also as a percentage, must not resolve below rollingUpdate.minReplicas")
	requireRejected(t, tc, coherentName, "rollingUpdate.minReplicas 2 with maxUnavailable 1", "resolves to 1, which must not be less than rollingUpdate.minReplicas (2)",
		setWorkerRollingUpdate(ptr.To[int32](2), nil))
	requireRejected(t, tc, coherentName, "rollingUpdate.minReplicas 2 with maxUnavailable 50% of 3", "which must not be less than rollingUpdate.minReplicas (2)",
		setWorkerRollingUpdate(ptr.To[int32](2), ptr.To(intstr.FromString("50%"))))
	requireAccepted(t, tc, coherentName, "rollingUpdate.minReplicas 2 with maxUnavailable 2",
		setWorkerRollingUpdate(ptr.To[int32](2), ptr.To(intstr.FromInt32(2))))

	tests.Logger.Info("3. Coherent: lowering init.minReplicas below rollingUpdate.minReplicas is rejected unless both are lowered")
	requireRejected(t, tc, coherentName, "init.minReplicas 1 with rollingUpdate.minReplicas 2", "must not be greater than min(init.minReplicas, replicas) (1)",
		setWorkerInit(1))
	requireAccepted(t, tc, coherentName, "init.minReplicas and rollingUpdate.minReplicas 1",
		setWorkerInit(1), setWorkerRollingUpdate(ptr.To[int32](1), nil))

	tests.Logger.Info("4. RollingRecreate: maxUnavailable must be at least 1, and rollingUpdate.minReplicas must not be set")
	requireRejected(t, tc, rollingName, "maxUnavailable 0", "must be greater than 0",
		setWorkerRollingUpdate(nil, ptr.To(intstr.FromInt32(0))))
	requireRejected(t, tc, rollingName, "maxUnavailable 0%", "must be a percentage between 1% and 100%",
		setWorkerRollingUpdate(nil, ptr.To(intstr.FromString("0%"))))
	requireRejected(t, tc, rollingName, "rollingUpdate.minReplicas under RollingRecreate", "must not be set when the update strategy is RollingRecreate",
		setWorkerRollingUpdate(ptr.To[int32](1), nil))
}
