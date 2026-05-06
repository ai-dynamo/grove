//go:build e2e

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

package update

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/workload"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	"gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	twoHashCompatWorkloadName = "two-hash-compat"
	alpha8Version             = "v0.1.0-alpha.8"
	alpha8ChartRef            = "oci://ghcr.io/ai-dynamo/grove/grove-charts"
	alpha8ImageRepository     = "ghcr.io/ai-dynamo/grove/grove-operator"
	alpha8CRDImageRepository  = "ghcr.io/ai-dynamo/grove/grove-install-crds"
	alpha8InitImageRepository = "ghcr.io/ai-dynamo/grove/grove-initc"
)

// Test_RU13_TwoHashCompatibilityWindow performs a literal operator upgrade from
// v0.1.0-alpha.8 to this branch. Existing pods/resources with legacy hashes must
// be migrated in place to canonical hashes without workload pod recreation.
func Test_RU13_TwoHashCompatibilityWindow(t *testing.T) {
	ctx := context.Background()
	tc, clusterCleanup := testctx.PrepareTest(ctx, t, 2,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         twoHashCompatWorkloadName,
			YAMLPath:     "../../yaml/workload-two-hash-compat.yaml",
			Namespace:    "default",
			ExpectedPods: 2,
		}),
	)

	localChartDir, err := setup.GetGroveChartDir()
	if err != nil {
		clusterCleanup()
		t.Fatalf("failed to locate local Grove chart: %v", err)
	}
	localChartVersion, err := readChartVersion(localChartDir)
	if err != nil {
		clusterCleanup()
		t.Fatalf("failed to read local Grove chart version: %v", err)
	}
	localValues, err := captureCurrentOperatorImageValues(ctx, tc.Client)
	if err != nil {
		clusterCleanup()
		t.Fatalf("failed to capture current operator image values: %v", err)
	}

	usingLocalOperator := true
	var tracker *updateTracker
	defer func() {
		if tracker != nil {
			tracker.stop()
		}
		if !usingLocalOperator {
			if restoreErr := upgradeOperator(ctx, tc, localChartDir, localChartVersion, true, localValues); restoreErr != nil {
				t.Errorf("failed to restore local Grove operator: %v", restoreErr)
			}
		}
		clusterCleanup()
	}()

	tests.Logger.Info("1. Downgrade Grove operator to v0.1.0-alpha.8")
	if err := upgradeOperator(ctx, tc, alpha8ChartRef, alpha8Version, false, alpha8HelmValues()); err != nil {
		t.Fatalf("failed to downgrade Grove operator to %s: %v", alpha8Version, err)
	}
	usingLocalOperator = false

	tests.Logger.Info("2. Deploy workload and verify alpha.8 stored legacy hashes")
	if _, err := tc.ApplyYAMLFile(tc.Workload.YAMLPath); err != nil {
		t.Fatalf("failed to apply compatibility workload: %v", err)
	}
	if err := tc.WaitForPods(tc.Workload.ExpectedPods); err != nil {
		t.Fatalf("failed to wait for compatibility workload pods: %v", err)
	}
	if err := waitForHashState(tc, assertLegacyHashState); err != nil {
		t.Fatalf("workload did not converge to expected legacy hash state: %v", err)
	}

	before, err := capturePodIdentities(tc)
	if err != nil {
		t.Fatalf("failed to capture pod identities before upgrade: %v", err)
	}

	tests.Logger.Info("3. Watch workload pods, then upgrade Grove operator back to this branch")
	tracker = newUpdateTracker()
	if err := tracker.start(tc); err != nil {
		t.Fatalf("failed to start pod update tracker: %v", err)
	}
	if err := upgradeOperator(ctx, tc, localChartDir, localChartVersion, true, localValues); err != nil {
		t.Fatalf("failed to upgrade Grove operator back to local branch: %v", err)
	}
	usingLocalOperator = true

	tests.Logger.Info("4. Trigger a no-op PodCliqueSet reconcile and wait for canonical hash migration")
	workloadManager := workload.NewWorkloadManager(tc.Client, tests.Logger)
	if err := workloadManager.TriggerPCSReconcile(tc.Ctx, tc.Namespace, tc.Workload.Name, fmt.Sprintf("two-hash-%d", time.Now().UnixNano())); err != nil {
		t.Fatalf("failed to trigger no-op PCS reconcile: %v", err)
	}
	if err := waitForHashState(tc, assertCanonicalHashState); err != nil {
		t.Fatalf("workload did not migrate to canonical hash state: %v", err)
	}
	if err := tc.WaitForPods(tc.Workload.ExpectedPods); err != nil {
		t.Fatalf("failed to wait for pods after hash migration: %v", err)
	}

	tracker.stop()
	events := tracker.getEvents()
	verifyNoPodReplacementsDuringHashMigration(t, events, before)
	if err := verifyPodIdentitiesUnchanged(tc, before); err != nil {
		t.Fatalf("pod identities changed during hash migration: %v", err)
	}

	tests.Logger.Info("Two-hash compatibility window upgrade test completed successfully!")
}

type chartYAML struct {
	Version string `yaml:"version"`
}

func readChartVersion(chartDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(chartDir, "Chart.yaml"))
	if err != nil {
		return "", err
	}
	var chart chartYAML
	if err := yaml.Unmarshal(data, &chart); err != nil {
		return "", err
	}
	if chart.Version == "" {
		return "", fmt.Errorf("version missing from Chart.yaml")
	}
	return chart.Version, nil
}

func alpha8HelmValues() map[string]interface{} {
	return map[string]interface{}{
		"image": map[string]interface{}{
			"repository": alpha8ImageRepository,
			"tag":        alpha8Version,
			"pullPolicy": string(corev1.PullIfNotPresent),
		},
		"crdInstaller": map[string]interface{}{
			"image": map[string]interface{}{
				"repository": alpha8CRDImageRepository,
				"tag":        alpha8Version,
				"pullPolicy": string(corev1.PullIfNotPresent),
			},
		},
		"deployment": map[string]interface{}{
			"env": []map[string]string{
				{
					"name":  "GROVE_INIT_CONTAINER_IMAGE",
					"value": alpha8InitImageRepository,
				},
			},
		},
	}
}

func upgradeOperator(ctx context.Context, tc *testctx.TestContext, chartRef, chartVersion string, reuseValues bool, values map[string]interface{}) error {
	_, err := setup.UpgradeHelmChart(&setup.HelmInstallConfig{
		RestConfig:     tc.Client.RestConfig,
		ReleaseName:    setup.OperatorDeploymentName,
		ChartRef:       chartRef,
		ChartVersion:   chartVersion,
		Namespace:      setup.OperatorNamespace,
		Wait:           true,
		ReuseValues:    reuseValues,
		Values:         values,
		Timeout:        3 * time.Minute,
		HelmLoggerFunc: tests.Logger.Debugf,
		Logger:         tests.Logger,
	})
	if err != nil {
		return err
	}
	return waitForOperatorReady(ctx, tc)
}

func waitForOperatorReady(ctx context.Context, tc *testctx.TestContext) error {
	podManager := pods.NewPodManager(tc.Client, tests.Logger)
	return podManager.WaitForReady(ctx, []string{setup.OperatorNamespace}, setup.OperatorPodLabelSelector, 1, 3*time.Minute, 5*time.Second)
}

func captureCurrentOperatorImageValues(ctx context.Context, cl client.Client) (map[string]interface{}, error) {
	var deployment appsv1.Deployment
	if err := cl.Get(ctx, types.NamespacedName{Namespace: setup.OperatorNamespace, Name: setup.OperatorDeploymentName}, &deployment); err != nil {
		return nil, err
	}

	operatorContainer, ok := findContainer(deployment.Spec.Template.Spec.Containers, "grove-operator")
	if !ok {
		return nil, fmt.Errorf("grove-operator container not found in deployment")
	}
	imageValues, err := imageValueMap(operatorContainer.Image, operatorContainer.ImagePullPolicy)
	if err != nil {
		return nil, fmt.Errorf("parse operator image %q: %w", operatorContainer.Image, err)
	}

	values := map[string]interface{}{
		"image": imageValues,
	}

	if crdInstaller, ok := findContainer(deployment.Spec.Template.Spec.InitContainers, "crd-installer"); ok {
		crdInstallerImage, err := imageValueMap(crdInstaller.Image, crdInstaller.ImagePullPolicy)
		if err != nil {
			return nil, fmt.Errorf("parse crd-installer image %q: %w", crdInstaller.Image, err)
		}
		values["crdInstaller"] = map[string]interface{}{
			"enabled": true,
			"image":   crdInstallerImage,
		}
	}

	if initImage, ok := findEnvValue(operatorContainer.Env, "GROVE_INIT_CONTAINER_IMAGE"); ok {
		values["deployment"] = map[string]interface{}{
			"env": []map[string]string{
				{
					"name":  "GROVE_INIT_CONTAINER_IMAGE",
					"value": initImage,
				},
			},
		}
	}

	return values, nil
}

func findContainer(containers []corev1.Container, name string) (corev1.Container, bool) {
	for _, container := range containers {
		if container.Name == name {
			return container, true
		}
	}
	return corev1.Container{}, false
}

func findEnvValue(env []corev1.EnvVar, name string) (string, bool) {
	for _, item := range env {
		if item.Name == name {
			return item.Value, true
		}
	}
	return "", false
}

func imageValueMap(image string, pullPolicy corev1.PullPolicy) (map[string]interface{}, error) {
	repository, tag, err := splitImageForHelm(image)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"repository": repository,
		"tag":        tag,
		"pullPolicy": string(pullPolicy),
	}, nil
}

func splitImageForHelm(image string) (string, string, error) {
	base, digest, hasDigest := strings.Cut(image, "@")
	tagSeparator := strings.LastIndex(base, ":")
	lastSlash := strings.LastIndex(base, "/")
	if tagSeparator > lastSlash {
		tag := base[tagSeparator+1:]
		if hasDigest {
			tag += "@" + digest
		}
		return base[:tagSeparator], tag, nil
	}
	if hasDigest {
		return base, digest, nil
	}
	return "", "", fmt.Errorf("image has no tag or digest")
}

type podIdentity struct {
	uid           types.UID
	restartCounts map[string]int32
}

func capturePodIdentities(tc *testctx.TestContext) (map[string]podIdentity, error) {
	podList, err := tc.ListPods()
	if err != nil {
		return nil, err
	}
	identities := make(map[string]podIdentity, len(podList.Items))
	for _, pod := range podList.Items {
		identities[pod.Name] = podIdentity{
			uid:           pod.UID,
			restartCounts: restartCountsByContainer(pod),
		}
	}
	return identities, nil
}

func restartCountsByContainer(pod corev1.Pod) map[string]int32 {
	counts := make(map[string]int32, len(pod.Status.ContainerStatuses))
	for _, status := range pod.Status.ContainerStatuses {
		counts[status.Name] = status.RestartCount
	}
	return counts
}

func verifyNoPodReplacementsDuringHashMigration(t *testing.T, events []podEvent, before map[string]podIdentity) {
	t.Helper()

	var deleted []string
	var added []string
	for _, event := range events {
		switch event.eventType {
		case watch.Deleted:
			if _, existed := before[event.pod.Name]; existed {
				deleted = append(deleted, event.pod.Name)
			}
		case watch.Added:
			if _, existed := before[event.pod.Name]; !existed {
				added = append(added, event.pod.Name)
			}
		}
	}
	if len(deleted) > 0 || len(added) > 0 {
		t.Fatalf("hash migration recreated workload pods: deleted=%v added=%v", deleted, added)
	}
}

func verifyPodIdentitiesUnchanged(tc *testctx.TestContext, before map[string]podIdentity) error {
	after, err := capturePodIdentities(tc)
	if err != nil {
		return err
	}
	if len(after) != len(before) {
		return fmt.Errorf("pod count changed: before=%d after=%d", len(before), len(after))
	}
	for podName, beforeIdentity := range before {
		afterIdentity, ok := after[podName]
		if !ok {
			return fmt.Errorf("pod %s missing after migration", podName)
		}
		if afterIdentity.uid != beforeIdentity.uid {
			return fmt.Errorf("pod %s UID changed from %s to %s", podName, beforeIdentity.uid, afterIdentity.uid)
		}
		for containerName, beforeRestarts := range beforeIdentity.restartCounts {
			afterRestarts, ok := afterIdentity.restartCounts[containerName]
			if !ok {
				return fmt.Errorf("pod %s container %s missing after migration", podName, containerName)
			}
			if afterRestarts != beforeRestarts {
				return fmt.Errorf("pod %s container %s restart count changed from %d to %d", podName, containerName, beforeRestarts, afterRestarts)
			}
		}
	}
	return nil
}

type hashAssertion func(*hashState) error

type hashState struct {
	pcs        *grovev1alpha1.PodCliqueSet
	pclqs      []grovev1alpha1.PodClique
	pcsgs      []grovev1alpha1.PodCliqueScalingGroup
	pods       []corev1.Pod
	pcsHash    componentutils.HashCandidates
	pclqHashes map[string]componentutils.HashCandidates
}

func waitForHashState(tc *testctx.TestContext, assertion hashAssertion) error {
	var lastAssertionErr error
	var lastState *hashState
	fetch := waiter.FetchFunc[*hashState](func(ctx context.Context) (*hashState, error) {
		state, err := fetchHashState(ctx, tc)
		if err != nil {
			return nil, err
		}
		lastState = state
		if err := assertion(state); err != nil {
			lastAssertionErr = err
			tests.Logger.Debugf("[two-hash-compat] hash state not ready: %v", err)
			return nil, nil
		}
		lastAssertionErr = nil
		return state, nil
	})
	w := waiter.New[*hashState]().
		WithTimeout(3 * time.Minute).
		WithInterval(5 * time.Second).
		WithRetryOnError().
		WithLogger(tests.Logger)
	_, err := w.WaitFor(tc.Ctx, fetch, func(state *hashState) bool { return state != nil })
	if err != nil {
		if lastAssertionErr != nil {
			return fmt.Errorf("%w; last assertion mismatch: %v; last observed hash state:\n%s", err, lastAssertionErr, formatHashStateSnapshot(lastState))
		}
		if lastState != nil {
			return fmt.Errorf("%w; last observed hash state:\n%s", err, formatHashStateSnapshot(lastState))
		}
	}
	return err
}

func fetchHashState(ctx context.Context, tc *testctx.TestContext) (*hashState, error) {
	pcs := &grovev1alpha1.PodCliqueSet{}
	if err := tc.Client.Get(ctx, types.NamespacedName{Namespace: tc.Namespace, Name: tc.Workload.Name}, pcs); err != nil {
		return nil, err
	}

	var pclqList grovev1alpha1.PodCliqueList
	if err := tc.Client.List(ctx, &pclqList, client.InNamespace(tc.Namespace), client.MatchingLabels(apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(tc.Workload.Name))); err != nil {
		return nil, err
	}
	var pcsgList grovev1alpha1.PodCliqueScalingGroupList
	if err := tc.Client.List(ctx, &pcsgList, client.InNamespace(tc.Namespace), client.MatchingLabels(apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(tc.Workload.Name))); err != nil {
		return nil, err
	}
	podList, err := tc.ListPodsWithContext(ctx)
	if err != nil {
		return nil, err
	}

	pclqHashes := make(map[string]componentutils.HashCandidates, len(pclqList.Items))
	for _, pclq := range pclqList.Items {
		candidates, err := componentutils.GetExpectedPCLQPodTemplateHashCandidates(pcs, pclq.ObjectMeta)
		if err != nil {
			return nil, err
		}
		pclqHashes[pclq.Name] = candidates
	}

	return &hashState{
		pcs:        pcs,
		pclqs:      pclqList.Items,
		pcsgs:      pcsgList.Items,
		pods:       podList.Items,
		pcsHash:    componentutils.ComputePCSGenerationHashCandidates(pcs),
		pclqHashes: pclqHashes,
	}, nil
}

func formatHashStateSnapshot(state *hashState) string {
	if state == nil {
		return "hash state was not fetched successfully"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "PCS %s currentGenerationHash=%s expectedPCS={canonical:%s legacy:%s} updateProgress=%t replicas=%d updated=%d available=%d\n",
		state.pcs.Name,
		stringValue(state.pcs.Status.CurrentGenerationHash),
		state.pcsHash.Canonical,
		state.pcsHash.Legacy,
		state.pcs.Status.UpdateProgress != nil,
		state.pcs.Status.Replicas,
		state.pcs.Status.UpdatedReplicas,
		state.pcs.Status.AvailableReplicas)

	pclqs := append([]grovev1alpha1.PodClique(nil), state.pclqs...)
	sort.Slice(pclqs, func(i, j int) bool { return pclqs[i].Name < pclqs[j].Name })
	for _, pclq := range pclqs {
		candidates := state.pclqHashes[pclq.Name]
		fmt.Fprintf(&b, "PCLQ %s labelHash=%s currentPodTemplateHash=%s currentPCSGenerationHash=%s expectedTemplate={canonical:%s legacy:%s} updateProgress=%t replicas=%d ready=%d updated=%d\n",
			pclq.Name,
			labelValue(pclq.Labels, apicommon.LabelPodTemplateHash),
			stringValue(pclq.Status.CurrentPodTemplateHash),
			stringValue(pclq.Status.CurrentPodCliqueSetGenerationHash),
			candidates.Canonical,
			candidates.Legacy,
			pclq.Status.UpdateProgress != nil,
			pclq.Status.Replicas,
			pclq.Status.ReadyReplicas,
			pclq.Status.UpdatedReplicas)
	}

	pcsgs := append([]grovev1alpha1.PodCliqueScalingGroup(nil), state.pcsgs...)
	sort.Slice(pcsgs, func(i, j int) bool { return pcsgs[i].Name < pcsgs[j].Name })
	for _, pcsg := range pcsgs {
		fmt.Fprintf(&b, "PCSG %s currentPCSGenerationHash=%s expectedPCS={canonical:%s legacy:%s} updateProgress=%t replicas=%d scheduled=%d available=%d updated=%d\n",
			pcsg.Name,
			stringValue(pcsg.Status.CurrentPodCliqueSetGenerationHash),
			state.pcsHash.Canonical,
			state.pcsHash.Legacy,
			pcsg.Status.UpdateProgress != nil,
			pcsg.Status.Replicas,
			pcsg.Status.ScheduledReplicas,
			pcsg.Status.AvailableReplicas,
			pcsg.Status.UpdatedReplicas)
	}

	pods := append([]corev1.Pod(nil), state.pods...)
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	for _, pod := range pods {
		fmt.Fprintf(&b, "Pod %s pclq=%s labelHash=%s phase=%s ready=%t node=%s\n",
			pod.Name,
			labelValue(pod.Labels, apicommon.LabelPodClique),
			labelValue(pod.Labels, apicommon.LabelPodTemplateHash),
			pod.Status.Phase,
			isPodReady(pod),
			pod.Spec.NodeName)
	}

	return strings.TrimSpace(b.String())
}

func stringValue(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}

func labelValue(labels map[string]string, key string) string {
	if labels == nil {
		return "<nil>"
	}
	value, ok := labels[key]
	if !ok {
		return "<missing>"
	}
	return value
}

func isPodReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func assertLegacyHashState(state *hashState) error {
	if err := requireLegacyHash("PCS status currentGenerationHash", state.pcs.Status.CurrentGenerationHash, state.pcsHash); err != nil {
		return err
	}
	if len(state.pclqs) != 2 {
		return fmt.Errorf("expected 2 PodCliques, got %d", len(state.pclqs))
	}
	for _, pclq := range state.pclqs {
		candidates := state.pclqHashes[pclq.Name]
		if err := requireLegacyHash(fmt.Sprintf("PCLQ %s label", pclq.Name), stringPtrFromMap(pclq.Labels, apicommon.LabelPodTemplateHash), candidates); err != nil {
			return err
		}
		if err := requireLegacyHash(fmt.Sprintf("PCLQ %s status currentPodTemplateHash", pclq.Name), pclq.Status.CurrentPodTemplateHash, candidates); err != nil {
			return err
		}
		if err := requireLegacyHash(fmt.Sprintf("PCLQ %s status currentPodCliqueSetGenerationHash", pclq.Name), pclq.Status.CurrentPodCliqueSetGenerationHash, state.pcsHash); err != nil {
			return err
		}
	}
	if len(state.pcsgs) != 1 {
		return fmt.Errorf("expected 1 PodCliqueScalingGroup, got %d", len(state.pcsgs))
	}
	for _, pcsg := range state.pcsgs {
		if err := requireLegacyHash(fmt.Sprintf("PCSG %s status currentPodCliqueSetGenerationHash", pcsg.Name), pcsg.Status.CurrentPodCliqueSetGenerationHash, state.pcsHash); err != nil {
			return err
		}
	}
	if len(state.pods) != 2 {
		return fmt.Errorf("expected 2 pods, got %d", len(state.pods))
	}
	for _, pod := range state.pods {
		pclqName := pod.Labels[apicommon.LabelPodClique]
		candidates, ok := state.pclqHashes[pclqName]
		if !ok {
			return fmt.Errorf("pod %s references unexpected PodClique %q", pod.Name, pclqName)
		}
		if err := requireLegacyHash(fmt.Sprintf("Pod %s label", pod.Name), stringPtrFromMap(pod.Labels, apicommon.LabelPodTemplateHash), candidates); err != nil {
			return err
		}
	}
	return nil
}

func assertCanonicalHashState(state *hashState) error {
	if err := requireCanonicalHash("PCS status currentGenerationHash", state.pcs.Status.CurrentGenerationHash, state.pcsHash); err != nil {
		return err
	}
	if state.pcs.Status.UpdateProgress != nil {
		return fmt.Errorf("PCS UpdateProgress was initialized during legacy hash migration")
	}
	if len(state.pclqs) != 2 {
		return fmt.Errorf("expected 2 PodCliques, got %d", len(state.pclqs))
	}
	for _, pclq := range state.pclqs {
		candidates := state.pclqHashes[pclq.Name]
		if err := requireCanonicalHash(fmt.Sprintf("PCLQ %s label", pclq.Name), stringPtrFromMap(pclq.Labels, apicommon.LabelPodTemplateHash), candidates); err != nil {
			return err
		}
		if err := requireCanonicalHash(fmt.Sprintf("PCLQ %s status currentPodTemplateHash", pclq.Name), pclq.Status.CurrentPodTemplateHash, candidates); err != nil {
			return err
		}
		if err := requireCanonicalHash(fmt.Sprintf("PCLQ %s status currentPodCliqueSetGenerationHash", pclq.Name), pclq.Status.CurrentPodCliqueSetGenerationHash, state.pcsHash); err != nil {
			return err
		}
	}
	if len(state.pcsgs) != 1 {
		return fmt.Errorf("expected 1 PodCliqueScalingGroup, got %d", len(state.pcsgs))
	}
	for _, pcsg := range state.pcsgs {
		if err := requireCanonicalHash(fmt.Sprintf("PCSG %s status currentPodCliqueSetGenerationHash", pcsg.Name), pcsg.Status.CurrentPodCliqueSetGenerationHash, state.pcsHash); err != nil {
			return err
		}
	}
	if len(state.pods) != 2 {
		return fmt.Errorf("expected 2 pods, got %d", len(state.pods))
	}
	for _, pod := range state.pods {
		pclqName := pod.Labels[apicommon.LabelPodClique]
		candidates, ok := state.pclqHashes[pclqName]
		if !ok {
			return fmt.Errorf("pod %s references unexpected PodClique %q", pod.Name, pclqName)
		}
		if err := requireCanonicalHash(fmt.Sprintf("Pod %s label", pod.Name), stringPtrFromMap(pod.Labels, apicommon.LabelPodTemplateHash), candidates); err != nil {
			return err
		}
	}
	return nil
}

func requireLegacyHash(name string, value *string, candidates componentutils.HashCandidates) error {
	if candidates.Canonical == candidates.Legacy {
		return fmt.Errorf("%s canonical and legacy hashes unexpectedly match: %s", name, candidates.Canonical)
	}
	if value == nil {
		return fmt.Errorf("%s is nil", name)
	}
	if *value != candidates.Legacy {
		return fmt.Errorf("%s = %s, want legacy %s and not canonical %s", name, *value, candidates.Legacy, candidates.Canonical)
	}
	return nil
}

func requireCanonicalHash(name string, value *string, candidates componentutils.HashCandidates) error {
	if candidates.Canonical == candidates.Legacy {
		return fmt.Errorf("%s canonical and legacy hashes unexpectedly match: %s", name, candidates.Canonical)
	}
	if value == nil {
		return fmt.Errorf("%s is nil", name)
	}
	if *value != candidates.Canonical {
		return fmt.Errorf("%s = %s, want canonical %s", name, *value, candidates.Canonical)
	}
	return nil
}

func stringPtrFromMap(values map[string]string, key string) *string {
	value, ok := values[key]
	if !ok {
		return nil
	}
	return &value
}
