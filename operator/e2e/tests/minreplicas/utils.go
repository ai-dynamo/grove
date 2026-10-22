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
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/podgang"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/kwok"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	testNamespace = "default"
	workerClique  = "worker"
	// recreateTimeout bounds a wait for a recreate, a rolling update or a gang termination to finish.
	recreateTimeout = 5 * time.Minute
	pollInterval    = 1 * time.Second
	// sampleInterval is how often an observer samples state. It is well below the time a replica takes to come
	// back, so no step of a recreate is missed.
	sampleInterval = 500 * time.Millisecond
	// holdDelay keeps a pod NotReady for longer than any test runs.
	holdDelay = time.Hour
	// updateTriggerEnv is the container env var that a pod template change sets.
	updateTriggerEnv = "UPDATE_TRIGGER"
)

var alphaPCSGVK = schema.GroupVersionKind{Group: "grove.io", Version: "v1alpha1", Kind: "PodCliqueSet"}

// cliqueSpec describes a clique template of a test PodCliqueSet.
type cliqueSpec struct {
	name     string
	replicas int32
	init     int32
	// availability is availability.minReplicas; 0 leaves availability unset.
	availability int32
	// breachTolerance is availability.breachTolerance; 0 keeps the default.
	breachTolerance time.Duration
	// autoScaleMax is autoScalingConfig.maxReplicas; 0 leaves autoScalingConfig unset.
	autoScaleMax int32
}

// scalingGroupSpec describes a PodCliqueScalingGroup config of a test PodCliqueSet.
type scalingGroupSpec struct {
	name            string
	cliqueNames     []string
	replicas        int32
	init            int32
	availability    int32
	breachTolerance time.Duration
}

// newPCS returns a v1beta1 PodCliqueSet whose pods each fill one KWOK node.
func newPCS(name string, replicas int32, strategy grovev1beta1.UpdateStrategyType, cliques []cliqueSpec, groups ...scalingGroupSpec) *grovev1beta1.PodCliqueSet {
	pcs := &grovev1beta1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: grovev1beta1.PodCliqueSetSpec{
			Replicas:       replicas,
			UpdateStrategy: &grovev1beta1.PodCliqueSetUpdateStrategy{Type: strategy},
		},
	}
	for _, c := range cliques {
		template := &grovev1beta1.PodCliqueTemplateSpec{
			Name:   c.name,
			Labels: map[string]string{"kai.scheduler/queue": "test"},
			Spec: grovev1beta1.PodCliqueSpec{
				RoleName:    c.name,
				Replicas:    c.replicas,
				Init:        &grovev1beta1.InitSpec{MinReplicas: ptr.To(c.init)},
				Termination: newTermination(c.availability, c.breachTolerance),
				PodSpec:     newPodSpec(c.name),
			},
		}
		if c.autoScaleMax > 0 {
			template.Spec.ScaleConfig = &grovev1beta1.AutoScalingConfig{MaxReplicas: c.autoScaleMax}
		}
		pcs.Spec.Template.Cliques = append(pcs.Spec.Template.Cliques, template)
	}
	for _, g := range groups {
		pcs.Spec.Template.PodCliqueScalingGroupConfigs = append(pcs.Spec.Template.PodCliqueScalingGroupConfigs, grovev1beta1.PodCliqueScalingGroupConfig{
			Name:        g.name,
			CliqueNames: g.cliqueNames,
			Replicas:    ptr.To(g.replicas),
			Init:        &grovev1beta1.InitSpec{MinReplicas: ptr.To(g.init)},
			Termination: newTermination(g.availability, g.breachTolerance),
		})
	}
	return pcs
}

func newTermination(minReplicas int32, breachTolerance time.Duration) *grovev1beta1.TerminationSpec {
	if minReplicas == 0 {
		return nil
	}
	policy := &grovev1beta1.AvailabilityPolicy{MinReplicas: ptr.To(minReplicas)}
	if breachTolerance > 0 {
		policy.BreachTolerance = &metav1.Duration{Duration: breachTolerance}
	}
	return &grovev1beta1.TerminationSpec{Resource: &grovev1beta1.ResourceTerminationSpec{Availability: policy}}
}

func newPodSpec(containerName string) corev1.PodSpec {
	return corev1.PodSpec{
		TerminationGracePeriodSeconds: ptr.To[int64](0),
		SchedulerName:                 "kai-scheduler",
		Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key:      setup.WorkerNodeLabelKey,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{setup.WorkerNodeLabelValue},
				}},
			}}},
		}},
		Tolerations: []corev1.Toleration{{
			Key:      setup.WorkerNodeLabelKey,
			Operator: corev1.TolerationOpEqual,
			Value:    setup.WorkerNodeLabelValue,
			Effect:   corev1.TaintEffectNoSchedule,
		}},
		Containers: []corev1.Container{{
			Name:      containerName,
			Image:     "registry:5001/busybox:latest",
			Command:   []string{"sleep", "infinity"},
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("80Mi")}},
		}},
	}
}

// expectedPods returns the number of pods pcs runs.
func expectedPods(pcs *grovev1beta1.PodCliqueSet) int {
	memberOf := map[string]int32{}
	for _, g := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		for _, name := range g.CliqueNames {
			memberOf[name] = ptr.Deref(g.Replicas, 1)
		}
	}
	perReplica := 0
	for _, c := range pcs.Spec.Template.Cliques {
		groupReplicas, ok := memberOf[c.Name]
		if !ok {
			groupReplicas = 1
		}
		perReplica += int(c.Spec.Replicas * groupReplicas)
	}
	return int(pcs.Spec.Replicas) * perReplica
}

// prepare prepares the cluster with workerNodes schedulable nodes for a test of the PodCliqueSet pcsName.
func prepare(t *testing.T, workerNodes int, pcsName string) (*testctx.TestContext, func()) {
	t.Helper()
	return testctx.PrepareTest(t.Context(), t, workerNodes,
		testctx.WithWorkload(&testctx.WorkloadConfig{Name: pcsName, Namespace: testNamespace}),
		testctx.WithTimeout(recreateTimeout),
		testctx.WithInterval(pollInterval),
	)
}

// deploy creates pcs and waits until all of its pods are ready.
func deploy(t *testing.T, tc *testctx.TestContext, pcs *grovev1beta1.PodCliqueSet) {
	t.Helper()
	pods := expectedPods(pcs)
	require.NoError(t, tc.Client.Create(tc.Ctx, pcs), "creating PodCliqueSet %s", pcs.Name)
	waitForReadyPods(t, tc, pcs.Name, pods)
}

// listLivePods returns the pods of the PodCliqueSet pcsName that are not being deleted.
func listLivePods(ctx context.Context, tc *testctx.TestContext, pcsName string) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	if err := tc.Client.List(ctx, podList, client.InNamespace(testNamespace), client.MatchingLabels{common.LabelPartOfKey: pcsName}); err != nil {
		return nil, err
	}
	return slices.DeleteFunc(podList.Items, func(pod corev1.Pod) bool { return pod.DeletionTimestamp != nil }), nil
}

// waitForReadyPods waits until the PodCliqueSet pcsName has exactly count live pods and all of them are ready.
func waitForReadyPods(t *testing.T, tc *testctx.TestContext, pcsName string, count int) {
	t.Helper()
	err := wait.PollUntilContextTimeout(tc.Ctx, pollInterval, recreateTimeout, true, func(ctx context.Context) (bool, error) {
		pods, err := listLivePods(ctx, tc, pcsName)
		if err != nil {
			return false, err
		}
		return len(pods) == count && !slices.ContainsFunc(pods, func(pod corev1.Pod) bool { return !isPodReady(&pod) }), nil
	})
	require.NoError(t, err, "%s did not reach %d ready pods", pcsName, count)
}

func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// pcsMutation changes a PodCliqueSet before it is updated.
type pcsMutation func(pcs *grovev1beta1.PodCliqueSet)

// updatePCS applies mutations to the PodCliqueSet pcsName with a v1beta1 request, retrying on conflict. It
// returns the admission error, if any.
func updatePCS(tc *testctx.TestContext, pcsName string, mutations ...pcsMutation) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		pcs := &grovev1beta1.PodCliqueSet{}
		if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: testNamespace, Name: pcsName}, pcs); err != nil {
			return err
		}
		for _, mutate := range mutations {
			mutate(pcs)
		}
		return tc.Client.Update(tc.Ctx, pcs)
	})
}

// requireAccepted applies mutations and fails the test when the update is rejected.
func requireAccepted(t *testing.T, tc *testctx.TestContext, pcsName, what string, mutations ...pcsMutation) {
	t.Helper()
	require.NoError(t, updatePCS(tc, pcsName, mutations...), "%s must be accepted", what)
}

// requireRejected applies mutations and fails the test unless the update is rejected with message.
func requireRejected(t *testing.T, tc *testctx.TestContext, pcsName, what, message string, mutations ...pcsMutation) {
	t.Helper()
	requireRejectedError(t, updatePCS(tc, pcsName, mutations...), what, message)
}

func requireRejectedError(t *testing.T, err error, what, message string) {
	t.Helper()
	require.Error(t, err, "%s must be rejected", what)
	require.Contains(t, err.Error(), message, "%s was rejected for the wrong reason", what)
}

func cliqueTemplate(pcs *grovev1beta1.PodCliqueSet, name string) *grovev1beta1.PodCliqueTemplateSpec {
	for _, c := range pcs.Spec.Template.Cliques {
		if c.Name == name {
			return c
		}
	}
	panic(fmt.Sprintf("clique %s not found in PodCliqueSet %s", name, pcs.Name))
}

func scalingGroupConfig(pcs *grovev1beta1.PodCliqueSet, name string) *grovev1beta1.PodCliqueScalingGroupConfig {
	for i := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if pcs.Spec.Template.PodCliqueScalingGroupConfigs[i].Name == name {
			return &pcs.Spec.Template.PodCliqueScalingGroupConfigs[i]
		}
	}
	panic(fmt.Sprintf("scaling group %s not found in PodCliqueSet %s", name, pcs.Name))
}

func setWorkerInit(minReplicas int32) pcsMutation {
	return func(pcs *grovev1beta1.PodCliqueSet) {
		cliqueTemplate(pcs, workerClique).Spec.Init = &grovev1beta1.InitSpec{MinReplicas: ptr.To(minReplicas)}
	}
}

func setCliqueAvailability(name string, minReplicas int32) pcsMutation {
	return func(pcs *grovev1beta1.PodCliqueSet) {
		componentutils.Availability(cliqueTemplate(pcs, name).Spec.Termination).MinReplicas = ptr.To(minReplicas)
	}
}

func setCliqueBreachTolerance(name string, breachTolerance time.Duration) pcsMutation {
	return func(pcs *grovev1beta1.PodCliqueSet) {
		componentutils.Availability(cliqueTemplate(pcs, name).Spec.Termination).BreachTolerance = &metav1.Duration{Duration: breachTolerance}
	}
}

func setCliqueTermination(name string, termination *grovev1beta1.TerminationSpec) pcsMutation {
	return func(pcs *grovev1beta1.PodCliqueSet) {
		cliqueTemplate(pcs, name).Spec.Termination = termination
	}
}

// setWorkerRollingUpdate sets rollingUpdate.minReplicas of the worker clique and, when given, rollingUpdate.maxUnavailable.
func setWorkerRollingUpdate(minReplicas *int32, maxUnavailable *intstr.IntOrString) pcsMutation {
	return func(pcs *grovev1beta1.PodCliqueSet) {
		c := cliqueTemplate(pcs, workerClique)
		if c.RollingUpdate == nil {
			c.RollingUpdate = &grovev1beta1.RollingUpdateConfiguration{}
		}
		c.RollingUpdate.MinReplicas = minReplicas
		if maxUnavailable != nil {
			c.RollingUpdate.MaxUnavailable = maxUnavailable
		}
	}
}

func setGroupInit(name string, minReplicas int32) pcsMutation {
	return func(pcs *grovev1beta1.PodCliqueSet) {
		scalingGroupConfig(pcs, name).Init = &grovev1beta1.InitSpec{MinReplicas: ptr.To(minReplicas)}
	}
}

func setGroupAvailability(name string, minReplicas int32) pcsMutation {
	return func(pcs *grovev1beta1.PodCliqueSet) {
		componentutils.Availability(scalingGroupConfig(pcs, name).Termination).MinReplicas = ptr.To(minReplicas)
	}
}

func setGroupTermination(name string, termination *grovev1beta1.TerminationSpec) pcsMutation {
	return func(pcs *grovev1beta1.PodCliqueSet) {
		scalingGroupConfig(pcs, name).Termination = termination
	}
}

func setAutoScaleMin(name string, minReplicas int32) pcsMutation {
	return func(pcs *grovev1beta1.PodCliqueSet) {
		cliqueTemplate(pcs, name).Spec.ScaleConfig.MinReplicas = ptr.To(minReplicas)
	}
}

// setStrategy changes the update strategy. Leaving Coherent drops every rollingUpdate.minReplicas, which only
// Coherent allows. Switching to OnDelete drops every rollingUpdate, which OnDelete forbids.
func setStrategy(strategy grovev1beta1.UpdateStrategyType) pcsMutation {
	return func(pcs *grovev1beta1.PodCliqueSet) {
		pcs.Spec.UpdateStrategy = &grovev1beta1.PodCliqueSetUpdateStrategy{Type: strategy}
		strip := func(rollingUpdate **grovev1beta1.RollingUpdateConfiguration) {
			switch {
			case *rollingUpdate == nil:
			case strategy == grovev1beta1.OnDeleteStrategy:
				*rollingUpdate = nil
			case strategy != grovev1beta1.CoherentStrategy:
				(*rollingUpdate).MinReplicas = nil
			}
		}
		for _, c := range pcs.Spec.Template.Cliques {
			strip(&c.RollingUpdate)
		}
		for i := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
			strip(&pcs.Spec.Template.PodCliqueScalingGroupConfigs[i].RollingUpdate)
		}
	}
}

// newRevision returns a value for changePodTemplate that no earlier change used.
func newRevision() string {
	return strconv.FormatInt(time.Now().UnixNano(), 10)
}

// changePodTemplate sets the UPDATE_TRIGGER env var of the named cliques to value, which starts a rolling update.
func changePodTemplate(value string, cliqueNames ...string) pcsMutation {
	return func(pcs *grovev1beta1.PodCliqueSet) {
		for _, name := range cliqueNames {
			container := &cliqueTemplate(pcs, name).Spec.PodSpec.Containers[0]
			container.Env = slices.DeleteFunc(container.Env, func(env corev1.EnvVar) bool { return env.Name == updateTriggerEnv })
			container.Env = append(container.Env, corev1.EnvVar{Name: updateTriggerEnv, Value: value})
		}
	}
}

// podRevision returns the UPDATE_TRIGGER value of the pod's first container, or "" when it has none.
func podRevision(pod *corev1.Pod) string {
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name == updateTriggerEnv {
			return env.Value
		}
	}
	return ""
}

// waitForRollingUpdateComplete waits until the update of the PodCliqueSet pcsName has ended with every replica
// updated and every live pod ready, and every pod of workerClique, the clique whose pod template the tests
// change, carries revision. Pods of the other cliques keep their own pod template.
func waitForRollingUpdateComplete(t *testing.T, tc *testctx.TestContext, pcsName, revision string) {
	t.Helper()
	waitForPCS(t, tc, pcsName, "the rolling update to finish", func(pcs *grovev1beta1.PodCliqueSet) bool {
		progress := pcs.Status.UpdateProgress
		if pcs.Status.UpdatedReplicas != pcs.Spec.Replicas || (progress != nil && progress.UpdateEndedAt == nil) {
			return false
		}
		pods, err := listLivePods(tc.Ctx, tc, pcsName)
		return err == nil && len(pods) == expectedPods(pcs) &&
			!slices.ContainsFunc(pods, func(pod corev1.Pod) bool {
				inClique := strings.HasSuffix(pod.Labels[common.LabelPodClique], "-"+workerClique)
				return !isPodReady(&pod) || (inClique && podRevision(&pod) != revision)
			})
	})
}

func isUpdateInProgress(pcs *grovev1beta1.PodCliqueSet) bool {
	return pcs.Status.UpdateProgress != nil && pcs.Status.UpdateProgress.UpdateEndedAt == nil
}

func pclqName(pcsName string, replicaIndex int, cliqueName string) string {
	return fmt.Sprintf("%s-%d-%s", pcsName, replicaIndex, cliqueName)
}

func pcsgName(pcsName string, replicaIndex int, groupName string) string {
	return fmt.Sprintf("%s-%d-%s", pcsName, replicaIndex, groupName)
}

func getPCS(t *testing.T, tc *testctx.TestContext, pcsName string) *grovev1beta1.PodCliqueSet {
	t.Helper()
	pcs := &grovev1beta1.PodCliqueSet{}
	require.NoError(t, tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: testNamespace, Name: pcsName}, pcs))
	return pcs
}

func getPCLQ(t *testing.T, tc *testctx.TestContext, name string) *grovev1beta1.PodClique {
	t.Helper()
	pclq := &grovev1beta1.PodClique{}
	require.NoError(t, tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: testNamespace, Name: name}, pclq))
	return pclq
}

func getPCSG(t *testing.T, tc *testctx.TestContext, name string) *grovev1beta1.PodCliqueScalingGroup {
	t.Helper()
	pcsg := &grovev1beta1.PodCliqueScalingGroup{}
	require.NoError(t, tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: testNamespace, Name: name}, pcsg))
	return pcsg
}

// waitForPCS waits until predicate holds for the PodCliqueSet pcsName and returns it.
func waitForPCS(t *testing.T, tc *testctx.TestContext, pcsName, what string, predicate func(*grovev1beta1.PodCliqueSet) bool) *grovev1beta1.PodCliqueSet {
	t.Helper()
	var pcs *grovev1beta1.PodCliqueSet
	err := wait.PollUntilContextTimeout(tc.Ctx, pollInterval, recreateTimeout, true, func(ctx context.Context) (bool, error) {
		current := &grovev1beta1.PodCliqueSet{}
		if err := tc.Client.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: pcsName}, current); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		pcs = current
		return predicate(current), nil
	})
	require.NoError(t, err, "%s: timed out waiting for %s", pcsName, what)
	return pcs
}

// waitForPCLQ waits until predicate holds for the PodClique name and returns it.
func waitForPCLQ(t *testing.T, tc *testctx.TestContext, name, what string, predicate func(*grovev1beta1.PodClique) bool) *grovev1beta1.PodClique {
	t.Helper()
	var pclq *grovev1beta1.PodClique
	err := wait.PollUntilContextTimeout(tc.Ctx, pollInterval, recreateTimeout, true, func(ctx context.Context) (bool, error) {
		current := &grovev1beta1.PodClique{}
		if err := tc.Client.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: name}, current); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		pclq = current
		return predicate(current), nil
	})
	require.NoError(t, err, "%s: timed out waiting for %s", name, what)
	return pclq
}

// waitForPCSG waits until predicate holds for the PodCliqueScalingGroup name and returns it.
func waitForPCSG(t *testing.T, tc *testctx.TestContext, name, what string, predicate func(*grovev1beta1.PodCliqueScalingGroup) bool) *grovev1beta1.PodCliqueScalingGroup {
	t.Helper()
	var pcsg *grovev1beta1.PodCliqueScalingGroup
	err := wait.PollUntilContextTimeout(tc.Ctx, pollInterval, recreateTimeout, true, func(ctx context.Context) (bool, error) {
		current := &grovev1beta1.PodCliqueScalingGroup{}
		if err := tc.Client.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: name}, current); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		pcsg = current
		return predicate(current), nil
	})
	require.NoError(t, err, "%s: timed out waiting for %s", name, what)
	return pcsg
}

// requireStable fails the test if check reports a change at any point during d.
func requireStable(t *testing.T, d time.Duration, what string, check func() error) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		require.NoError(t, check(), what)
		time.Sleep(pollInterval)
	}
}

// workerUIDs returns the UID of the worker PodClique in each PodCliqueSet replica, or "" where it is missing.
func workerUIDs(t *testing.T, tc *testctx.TestContext, pcsName string, replicas int) []types.UID {
	t.Helper()
	uids := make([]types.UID, replicas)
	for i := range replicas {
		pclq := &grovev1beta1.PodClique{}
		if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: testNamespace, Name: pclqName(pcsName, i, workerClique)}, pclq); err == nil {
			uids[i] = pclq.UID
		}
	}
	return uids
}

func podUIDs(t *testing.T, tc *testctx.TestContext, pcsName string) []types.UID {
	t.Helper()
	pods, err := listLivePods(tc.Ctx, tc, pcsName)
	require.NoError(t, err)
	uids := make([]types.UID, 0, len(pods))
	for _, pod := range pods {
		uids = append(uids, pod.UID)
	}
	return uids
}

// livePodsOfReplica returns the live pods of the PodCliqueSet pcsName in the given replica.
func livePodsOfReplica(t *testing.T, tc *testctx.TestContext, pcsName string, replicaIndex int) []corev1.Pod {
	t.Helper()
	pods, err := listLivePods(tc.Ctx, tc, pcsName)
	require.NoError(t, err)
	return slices.DeleteFunc(pods, func(pod corev1.Pod) bool {
		return pod.Labels[common.LabelPodCliqueSetReplicaIndex] != strconv.Itoa(replicaIndex)
	})
}

func deletePods(t *testing.T, tc *testctx.TestContext, pods ...corev1.Pod) {
	t.Helper()
	for i := range pods {
		require.NoError(t, client.IgnoreNotFound(tc.Client.Delete(tc.Ctx, &pods[i])), "deleting pod %s", pods[i].Name)
	}
}

func gangShapeCondition(pcs *grovev1beta1.PodCliqueSet) *metav1.Condition {
	return meta.FindStatusCondition(pcs.Status.Conditions, apiconstants.ConditionTypeGangShapeRecreateInProgress)
}

func breachCondition(pclq *grovev1beta1.PodClique) *metav1.Condition {
	return meta.FindStatusCondition(pclq.Status.Conditions, apiconstants.ConditionTypeMinAvailableBreached)
}

func isBreached(pclq *grovev1beta1.PodClique) bool {
	cond := breachCondition(pclq)
	return cond != nil && cond.Status == metav1.ConditionTrue
}

func isNotBreached(pclq *grovev1beta1.PodClique) bool {
	cond := breachCondition(pclq)
	return cond != nil && cond.Status == metav1.ConditionFalse
}

// requireNoGangShapeRecreate fails the test if the PodCliqueSet reports a recreate in its status.
func requireNoGangShapeRecreate(t *testing.T, pcs *grovev1beta1.PodCliqueSet) {
	t.Helper()
	require.Nil(t, pcs.Status.GangShapeRecreate, "%s must not report a gang-shape recreate", pcs.Name)
	require.Nil(t, gangShapeCondition(pcs), "%s must not carry the %s condition", pcs.Name, apiconstants.ConditionTypeGangShapeRecreateInProgress)
}

// waitForGangShapeCurrent waits until every replica of the PodCliqueSet pcsName has been recreated to its current
// gang shape: the condition is False with reason GangShapeCurrent, status.gangShapeRecreate is cleared, every
// PodClique and PodCliqueScalingGroup carries the current hash and every pod is ready. It returns the PodCliqueSet.
func waitForGangShapeCurrent(t *testing.T, tc *testctx.TestContext, pcsName string) *grovev1beta1.PodCliqueSet {
	t.Helper()
	pcs := waitForPCS(t, tc, pcsName, "the gang-shape recreate to finish", func(pcs *grovev1beta1.PodCliqueSet) bool {
		cond := gangShapeCondition(pcs)
		if pcs.Status.GangShapeRecreate != nil || cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != apiconstants.ConditionReasonGangShapeCurrent {
			return false
		}
		stale, err := staleChildren(tc.Ctx, tc, pcs)
		return err == nil && len(stale) == 0
	})
	waitForReadyPods(t, tc, pcsName, expectedPods(pcs))
	return pcs
}

// staleChildren returns the live PodCliques and PodCliqueScalingGroups of pcs whose gang-shape hash label is not
// the current hash.
func staleChildren(ctx context.Context, tc *testctx.TestContext, pcs *grovev1beta1.PodCliqueSet) ([]string, error) {
	hash := componentutils.ComputeGangShapeHash(pcs)
	selector := client.MatchingLabels{common.LabelPartOfKey: pcs.Name}
	pclqs := &grovev1beta1.PodCliqueList{}
	if err := tc.Client.List(ctx, pclqs, client.InNamespace(testNamespace), selector); err != nil {
		return nil, err
	}
	pcsgs := &grovev1beta1.PodCliqueScalingGroupList{}
	if err := tc.Client.List(ctx, pcsgs, client.InNamespace(testNamespace), selector); err != nil {
		return nil, err
	}
	var stale []string
	for _, pclq := range pclqs.Items {
		if pclq.DeletionTimestamp == nil && pclq.Labels[common.LabelGangShapeHash] != hash {
			stale = append(stale, pclq.Name)
		}
	}
	for _, pcsg := range pcsgs.Items {
		if pcsg.DeletionTimestamp == nil && pcsg.Labels[common.LabelGangShapeHash] != hash {
			stale = append(stale, pcsg.Name)
		}
	}
	return stale, nil
}

// onceEach returns the deletion count of each replica in replicaIndices as 1.
func onceEach(replicaIndices ...int) map[int]int {
	deletions := make(map[int]int, len(replicaIndices))
	for _, i := range replicaIndices {
		deletions[i] = 1
	}
	return deletions
}

// requirePodGroupMinReplicas waits until every PodGang pod group of the PodClique cliqueName in the PodCliqueSet
// pcsName asks for minReplicas.
func requirePodGroupMinReplicas(t *testing.T, tc *testctx.TestContext, pcsName, cliqueName string, minReplicas int32) {
	t.Helper()
	verifier := podgang.NewVerifier(tc.Client, tests.Logger)
	nsName := types.NamespacedName{Namespace: testNamespace, Name: pcsName}
	suffix := "-" + cliqueName
	err := wait.PollUntilContextTimeout(tc.Ctx, pollInterval, recreateTimeout, true, func(ctx context.Context) (bool, error) {
		podGangs, err := verifier.List(ctx, nsName)
		if err != nil {
			return false, err
		}
		found := false
		for _, pg := range podGangs {
			for _, group := range pg.Spec.PodGroups {
				if !strings.HasSuffix(group.Name, suffix) {
					continue
				}
				found = true
				if group.MinReplicas != minReplicas {
					return false, nil
				}
			}
		}
		return found, nil
	})
	require.NoError(t, err, "PodGang pod groups of %s in %s did not reach minReplicas %d", cliqueName, pcsName, minReplicas)
}

// recreateObserver samples a PodCliqueSet while its replicas are recreated. It records the order in which
// replicas were picked, the condition reasons that were reported, whether updateProgress was ever set, the
// largest number of replicas that were unavailable at the same time, and every PodClique and pod UID seen in each
// replica.
type recreateObserver struct {
	tc      *testctx.TestContext
	pcsName string
	cancel  context.CancelFunc
	done    chan struct{}

	mu                 sync.Mutex
	order              []int
	reasons            sets.Set[string]
	updateProgressSeen bool
	maxUnavailable     int
	pclqUIDs           map[string]sets.Set[types.UID]
	podUIDs            map[string]sets.Set[types.UID]
}

func observeRecreate(tc *testctx.TestContext, pcsName string) *recreateObserver {
	ctx, cancel := context.WithCancel(tc.Ctx)
	o := &recreateObserver{
		tc: tc, pcsName: pcsName, cancel: cancel, done: make(chan struct{}),
		reasons:  sets.New[string](),
		pclqUIDs: map[string]sets.Set[types.UID]{},
		podUIDs:  map[string]sets.Set[types.UID]{},
	}
	go func() {
		defer close(o.done)
		ticker := time.NewTicker(sampleInterval)
		defer ticker.Stop()
		for {
			o.sample(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return o
}

// stop stops sampling. The recorded fields can be read once it returns.
func (o *recreateObserver) stop() {
	o.cancel()
	<-o.done
}

func (o *recreateObserver) sample(ctx context.Context) {
	pcs := &grovev1beta1.PodCliqueSet{}
	if err := o.tc.Client.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: o.pcsName}, pcs); err != nil {
		return
	}
	children, err := listChildren(ctx, o.tc, pcs.Name)
	if err != nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, pclq := range children.pclqs {
		insertUID(o.pclqUIDs, pclq.Name, pclq.UID)
	}
	for _, pod := range children.pods {
		insertUID(o.podUIDs, pod.Labels[common.LabelPodCliqueSetReplicaIndex], pod.UID)
	}
	if progress := pcs.Status.GangShapeRecreate; progress != nil && progress.CurrentlyRecreating != nil {
		index := int(*progress.CurrentlyRecreating)
		if len(o.order) == 0 || o.order[len(o.order)-1] != index {
			o.order = append(o.order, index)
		}
	}
	if cond := gangShapeCondition(pcs); cond != nil && cond.Status == metav1.ConditionTrue {
		o.reasons.Insert(cond.Reason)
	}
	o.updateProgressSeen = o.updateProgressSeen || pcs.Status.UpdateProgress != nil
	o.maxUnavailable = max(o.maxUnavailable, unavailableReplicas(pcs, children))
}

// workerDeletions returns how many times the observer saw the worker PodClique of each of the first replicas
// replicas deleted, leaving out replicas that were never deleted. It counts every deletion, whether by a recreate
// or by gang termination. The operator's events can't be counted instead, because the event recorder drops and
// aggregates the events of a busy PodCliqueSet. It can be called while the observer is running.
func (o *recreateObserver) workerDeletions(replicas int) map[int]int {
	o.mu.Lock()
	defer o.mu.Unlock()
	deletions := map[int]int{}
	for i := range replicas {
		if n := o.pclqUIDs[pclqName(o.pcsName, i, workerClique)].Len() - 1; n > 0 {
			deletions[i] = n
		}
	}
	return deletions
}

// distinctPods returns how many pods the observer saw in the replica.
func (o *recreateObserver) distinctPods(replicaIndex int) int {
	return o.podUIDs[strconv.Itoa(replicaIndex)].Len()
}

func insertUID(uids map[string]sets.Set[types.UID], key string, uid types.UID) {
	if uids[key] == nil {
		uids[key] = sets.New[types.UID]()
	}
	uids[key].Insert(uid)
}

// children holds the PodCliques, PodCliqueScalingGroups and pods of a PodCliqueSet.
type children struct {
	pclqs []grovev1beta1.PodClique
	pcsgs []grovev1beta1.PodCliqueScalingGroup
	pods  []corev1.Pod
}

func listChildren(ctx context.Context, tc *testctx.TestContext, pcsName string) (*children, error) {
	selector := client.MatchingLabels{common.LabelPartOfKey: pcsName}
	pclqs := &grovev1beta1.PodCliqueList{}
	if err := tc.Client.List(ctx, pclqs, client.InNamespace(testNamespace), selector); err != nil {
		return nil, err
	}
	pcsgs := &grovev1beta1.PodCliqueScalingGroupList{}
	if err := tc.Client.List(ctx, pcsgs, client.InNamespace(testNamespace), selector); err != nil {
		return nil, err
	}
	pods := &corev1.PodList{}
	if err := tc.Client.List(ctx, pods, client.InNamespace(testNamespace), selector); err != nil {
		return nil, err
	}
	return &children{pclqs: pclqs.Items, pcsgs: pcsgs.Items, pods: pods.Items}, nil
}

// unavailableReplicas counts the replicas of pcs that miss a standalone PodClique or a PodCliqueScalingGroup, have
// one being deleted, or have one below its ready threshold. It uses the operator's definition of an available
// replica.
func unavailableReplicas(pcs *grovev1beta1.PodCliqueSet, c *children) int {
	members := sets.New[string]()
	for _, g := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		members.Insert(g.CliqueNames...)
	}
	available := map[string]bool{}
	for _, pclq := range c.pclqs {
		available[pclq.Name] = pclq.DeletionTimestamp == nil && pclq.Status.ReadyReplicas >= componentutils.PCLQReadyThreshold(&pclq.Spec)
	}
	for _, pcsg := range c.pcsgs {
		available[pcsg.Name] = pcsg.DeletionTimestamp == nil && pcsg.Status.AvailableReplicas >= componentutils.PCSGReadyThreshold(&pcsg.Spec)
	}
	count := 0
	for i := range int(pcs.Spec.Replicas) {
		ok := true
		for _, c := range pcs.Spec.Template.Cliques {
			if !members.Has(c.Name) {
				ok = ok && available[pclqName(pcs.Name, i, c.Name)]
			}
		}
		for _, g := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
			ok = ok && available[pcsgName(pcs.Name, i, g.Name)]
		}
		if !ok {
			count++
		}
	}
	return count
}

// podStage is a KWOK Stage that takes over the fresh pods of some replicas of one PodCliqueSet.
type podStage struct {
	name     string
	pcsName  string
	replicas []int
	// crashLoop makes the pods report a container that exited non-zero, which breaches availability. Otherwise
	// the pods stay Pending and NotReady for delay, which leaves the replica unavailable without a breach.
	crashLoop bool
	delay     time.Duration
}

const stageManifest = `apiVersion: kwok.x-k8s.io/v1alpha1
kind: Stage
metadata:
  name: %s
spec:
  resourceRef:
    apiGroup: v1
    kind: Pod
  selector:
    matchExpressions:
      - key: .metadata.deletionTimestamp
        operator: DoesNotExist
      - key: .status.podIP
        operator: DoesNotExist
      - key: '.metadata.labels["app.kubernetes.io/part-of"]'
        operator: In
        values: [%q]
      - key: '.metadata.labels["grove.io/podcliqueset-replica-index"]'
        operator: In
        values: [%s]
  weight: 30
  delay:
    durationMilliseconds: %d
  next:
    statusSubresource: status
    statusTemplate: |
%s`

const readyStatusTemplate = `      {{ $now := Now }}
      conditions:
      - lastTransitionTime: {{ $now | Quote }}
        status: "True"
        type: Initialized
      - lastTransitionTime: {{ $now | Quote }}
        status: "True"
        type: Ready
      - lastTransitionTime: {{ $now | Quote }}
        status: "True"
        type: ContainersReady
      containerStatuses:
      {{ range .spec.containers }}
      - image: {{ .image | Quote }}
        name: {{ .name | Quote }}
        ready: true
        restartCount: 0
        state:
          running:
            startedAt: {{ $now | Quote }}
      {{ end }}
      hostIP: {{ NodeIPWith .spec.nodeName | Quote }}
      podIP: {{ PodIPWith .spec.nodeName ( or .spec.hostNetwork false ) ( or .metadata.uid "" ) ( or .metadata.name "" ) ( or .metadata.namespace "" ) | Quote }}
      phase: Running
      startTime: {{ $now | Quote }}
`

const crashLoopStatusTemplate = `      {{ $now := Now }}
      conditions:
      - lastTransitionTime: {{ $now | Quote }}
        status: "True"
        type: Initialized
      - lastTransitionTime: {{ $now | Quote }}
        status: "False"
        reason: ContainersNotReady
        type: Ready
      - lastTransitionTime: {{ $now | Quote }}
        status: "False"
        reason: ContainersNotReady
        type: ContainersReady
      containerStatuses:
      {{ range .spec.containers }}
      - image: {{ .image | Quote }}
        name: {{ .name | Quote }}
        ready: false
        restartCount: 3
        state:
          waiting:
            reason: CrashLoopBackOff
            message: "simulated crash loop"
        lastState:
          terminated:
            exitCode: 1
            reason: Error
            startedAt: {{ $now | Quote }}
            finishedAt: {{ $now | Quote }}
      {{ end }}
      hostIP: {{ NodeIPWith .spec.nodeName | Quote }}
      podIP: {{ PodIPWith .spec.nodeName ( or .spec.hostNetwork false ) ( or .metadata.uid "" ) ( or .metadata.name "" ) ( or .metadata.namespace "" ) | Quote }}
      phase: Running
      startTime: {{ $now | Quote }}
`

// holdStage keeps the fresh pods of the given replicas Pending and NotReady until it is released.
func holdStage(pcsName string, replicas ...int) podStage {
	return podStage{name: pcsName + "-hold", pcsName: pcsName, replicas: replicas, delay: holdDelay}
}

// crashLoopStage makes the fresh pods of the given replicas crash-loop until it is released.
func crashLoopStage(pcsName string, replicas ...int) podStage {
	return podStage{name: pcsName + "-crashloop", pcsName: pcsName, replicas: replicas, crashLoop: true}
}

func (s podStage) manifest() string {
	values := make([]string, 0, len(s.replicas))
	for _, i := range s.replicas {
		values = append(values, strconv.Quote(strconv.Itoa(i)))
	}
	statusTemplate := readyStatusTemplate
	if s.crashLoop {
		statusTemplate = crashLoopStatusTemplate
	}
	return fmt.Sprintf(stageManifest, s.name, s.pcsName, strings.Join(values, ", "), s.delay.Milliseconds(), statusTemplate)
}

// apply applies the stage and deletes it when the test ends.
func (s podStage) apply(t *testing.T, tc *testctx.TestContext) {
	t.Helper()
	require.NoError(t, kwok.ApplyStageManifest(tc.Ctx, tc.Client, s.manifest()), "applying KWOK stage %s", s.name)
	t.Cleanup(func() {
		if err := kwok.DeleteStage(context.WithoutCancel(tc.Ctx), tc.Client, s.name); err != nil {
			t.Errorf("deleting KWOK stage %s: %v", s.name, err)
		}
	})
}

// release deletes the stage, then deletes every not-ready pod of its replicas, so their replacements become ready
// through the default pod-ready stage. KWOK does not re-evaluate a pod that a removed stage still holds, and a
// crash-looping pod already has a podIP, which the default stage does not match.
func (s podStage) release(t *testing.T, tc *testctx.TestContext) {
	t.Helper()
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, s.name), "deleting KWOK stage %s", s.name)
	for _, i := range s.replicas {
		pods := livePodsOfReplica(t, tc, s.pcsName, i)
		deletePods(t, tc, slices.DeleteFunc(pods, func(pod corev1.Pod) bool { return isPodReady(&pod) })...)
	}
}

// getAlphaPCS reads the PodCliqueSet pcsName as grove.io/v1alpha1.
func getAlphaPCS(t *testing.T, tc *testctx.TestContext, pcsName string) *unstructured.Unstructured {
	t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(alphaPCSGVK)
	require.NoError(t, tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: testNamespace, Name: pcsName}, obj))
	return obj
}

// alphaMutation changes a PodCliqueSet read as v1alpha1 before it is updated.
type alphaMutation func(obj *unstructured.Unstructured) error

// updateAlphaPCS applies mutations to the PodCliqueSet pcsName with a grove.io/v1alpha1 request, retrying on
// conflict. It returns the admission error, if any.
func updateAlphaPCS(tc *testctx.TestContext, pcsName string, mutations ...alphaMutation) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(alphaPCSGVK)
		if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: testNamespace, Name: pcsName}, obj); err != nil {
			return err
		}
		for _, mutate := range mutations {
			if err := mutate(obj); err != nil {
				return err
			}
		}
		return tc.Client.Update(tc.Ctx, obj)
	})
}

// mutateAlphaListEntry applies mutate to the entry named name of the list at spec.template.<listField>.
func mutateAlphaListEntry(listField, name string, mutate func(entry map[string]any)) alphaMutation {
	return func(obj *unstructured.Unstructured) error {
		entries, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", listField)
		if err != nil || !found {
			return fmt.Errorf("spec.template.%s not found: %v", listField, err)
		}
		for _, entry := range entries {
			m, ok := entry.(map[string]any)
			if ok && m["name"] == name {
				mutate(m)
				return unstructured.SetNestedSlice(obj.Object, entries, "spec", "template", listField)
			}
		}
		return fmt.Errorf("spec.template.%s has no entry %s", listField, name)
	}
}

func setAlphaCliqueMinAvailable(name string, minAvailable int64) alphaMutation {
	return mutateAlphaListEntry("cliques", name, func(entry map[string]any) {
		entry["spec"].(map[string]any)["minAvailable"] = minAvailable
	})
}

func setAlphaCliqueMaxUnavailable(name string, maxUnavailable int64) alphaMutation {
	return mutateAlphaListEntry("cliques", name, func(entry map[string]any) {
		rollingUpdate, _ := entry["rollingUpdate"].(map[string]any)
		if rollingUpdate == nil {
			rollingUpdate = map[string]any{}
		}
		rollingUpdate["maxUnavailable"] = maxUnavailable
		entry["rollingUpdate"] = rollingUpdate
	})
}

func setAlphaGroupMinAvailable(name string, minAvailable int64) alphaMutation {
	return mutateAlphaListEntry("podCliqueScalingGroups", name, func(entry map[string]any) {
		entry["minAvailable"] = minAvailable
	})
}

func setAlphaGroupMaxUnavailable(name string, maxUnavailable int64) alphaMutation {
	return mutateAlphaListEntry("podCliqueScalingGroups", name, func(entry map[string]any) {
		rollingUpdate, _ := entry["rollingUpdate"].(map[string]any)
		if rollingUpdate == nil {
			rollingUpdate = map[string]any{}
		}
		rollingUpdate["maxUnavailable"] = maxUnavailable
		entry["rollingUpdate"] = rollingUpdate
	})
}

func setAlphaStrategy(strategy string) alphaMutation {
	return func(obj *unstructured.Unstructured) error {
		return unstructured.SetNestedField(obj.Object, strategy, "spec", "updateStrategy", "type")
	}
}

func setAlphaAnnotation(key, value string) alphaMutation {
	return func(obj *unstructured.Unstructured) error {
		annotations := obj.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[key] = value
		obj.SetAnnotations(annotations)
		return nil
	}
}
