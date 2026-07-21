//go:build e2e && e2eupgrade

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

// Package upgrade contains an end-to-end test for upgrading the Grove operator.
//
// These tests are disabled by default due to the 'e2e' and 'e2eupgrade' build tags above.
// To run these tests, use:
//
//	go test -tags=e2e,e2eupgrade ./e2e/tests/upgrade/...
//
// Without both build tags, these tests will be skipped entirely.

package upgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/diagnostics"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/k8sclient"
	e2elog "github.com/ai-dynamo/grove/operator/e2e/log"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/google/go-github/v86/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	githubOwner           = "ai-dynamo"
	githubRepository      = "grove"
	releasedChart         = "oci://ghcr.io/ai-dynamo/grove/grove-charts"
	releasedImageRegistry = "ghcr.io/ai-dynamo/grove"
	releaseName           = "grove"
	operatorNamespace     = setup.OperatorNamespace
	workloadNamespace     = "grove-upgrade-e2e"
	workloadName          = "upgrade-survivor"
	currentImageTag       = "upgrade-e2e-current"
)

var (
	podsGVR          = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	deploymentsGVR   = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	podCliqueSetsGVR = schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"}
)

type commandRunner struct {
	t           *testing.T
	workingDir  string
	environment []string
}

// TestUpgradeFromLatestGitHubRelease owns the complete upgrade lifecycle. It deliberately
// does not reuse the shared E2E cluster lifecycle: upgrade coverage should begin with the
// published chart and images, not with a cluster that already contains checkout resources.
func TestUpgradeFromLatestGitHubRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("upgrade E2E test is disabled by -short")
	}

	operatorDir := findOperatorDir(t)
	runner := &commandRunner{
		t:           t,
		workingDir:  operatorDir,
		environment: os.Environ(),
	}
	requireCommands(t, "docker", "k3d", "make")

	testTimeout := durationFromEnv(t, "GROVE_UPGRADE_E2E_TIMEOUT", 30*time.Minute)
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()

	fromVersion := strings.TrimSpace(os.Getenv("GROVE_UPGRADE_FROM_VERSION"))
	if fromVersion == "" {
		fromVersion = latestGitHubRelease(ctx, t, strings.TrimSpace(os.Getenv("GITHUB_TOKEN")))
	}
	t.Logf("testing Grove upgrade from %s to the current checkout", fromVersion)

	runner.mustRun(ctx, "docker", "info")
	runner.mustRun(ctx, "make",
		"docker-build",
		"PLATFORM=linux/"+runtime.GOARCH,
		"VERSION="+currentImageTag,
		"DOCKER_BUILD_ADDITIONAL_ARGS=--load",
	)

	clusterName := firstNonEmpty(
		os.Getenv("GROVE_UPGRADE_E2E_CLUSTER_NAME"),
		fmt.Sprintf("grove-upgrade-e2e-%d", os.Getpid()),
	)
	keepCluster := envBool("GROVE_UPGRADE_E2E_KEEP_CLUSTER")
	t.Cleanup(func() {
		if keepCluster {
			t.Logf("preserving k3d cluster %q", clusterName)
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if output, err := runner.run(cleanupCtx, "", "k3d", "cluster", "delete", clusterName); err != nil {
			t.Logf("failed to delete k3d cluster %q: %v\n%s", clusterName, err, output)
		}
	})

	k3sImage := firstNonEmpty(os.Getenv("GROVE_UPGRADE_E2E_K3S_IMAGE"), "rancher/k3s:v1.35.5-k3s1")
	runner.mustRun(ctx, "k3d", "cluster", "create", clusterName,
		"--servers", "1",
		"--agents", "0",
		"--image", k3sImage,
		"--no-lb",
		"--wait",
		"--timeout", "3m",
		"--k3s-arg", "--disable=traefik@server:0",
		"--kubeconfig-update-default=false",
		"--kubeconfig-switch-context=false",
	)

	kubeconfig := runner.mustRun(ctx, "k3d", "kubeconfig", "get", clusterName)
	restConfig, dynamicClient, diagnosticClient := newKubernetesClients(t, []byte(kubeconfig))
	diagnosticLogger := e2elog.NewTestLogger(e2elog.InfoLevel)

	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		t.Log("collecting upgrade E2E diagnostics")
		diagnosticCtx, diagnosticCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer diagnosticCancel()
		diagMode := os.Getenv(diagnostics.ModeEnvVar)
		if diagMode == "" {
			diagMode = diagnostics.ModeFile
		}
		diagDir := os.Getenv(diagnostics.DirEnvVar)
		diagnostics.NewDiagCollector(
			diagnosticClient, workloadNamespace, diagMode, diagDir, diagnosticLogger,
		).CollectAll(diagnosticCtx, t.Name())
	})

	runner.mustRun(ctx, "k3d", "image", "import",
		"grove-operator:"+currentImageTag,
		"grove-initc:"+currentImageTag,
		"grove-install-crds:"+currentImageTag,
		"--cluster", clusterName,
	)

	if !t.Run("install released operator", func(t *testing.T) {
		_, err := setup.InstallHelmChart(&setup.HelmInstallConfig{
			RestConfig:      restConfig,
			ReleaseName:     releaseName,
			ChartRef:        releasedChart,
			ChartVersion:    fromVersion,
			Namespace:       operatorNamespace,
			CreateNamespace: true,
			Wait:            true,
			Timeout:         6 * time.Minute,
			HelmLoggerFunc:  t.Logf,
		})
		require.NoError(t, err, "install released Grove chart")

		releasedOperatorImage := releasedImageRegistry + "/grove-operator:" + fromVersion
		assertDeployment(t, ctx, dynamicClient, releasedOperatorImage, "", "")
	}) {
		t.FailNow()
	}

	var originalPodUIDs []string
	if !t.Run("create workload before upgrade", func(t *testing.T) {
		applyManifestEventually(t, ctx, dynamicClient, diagnosticClient.RESTMapper(), compatibilityWorkload)
		pods := waitForWorkload(t, ctx, dynamicClient, 1, 5*time.Minute)
		for _, pod := range pods.Items {
			originalPodUIDs = append(originalPodUIDs, string(pod.GetUID()))
		}

		releasedInitImage := releasedImageRegistry + "/grove-initc:" + fromVersion
		require.Truef(t, podUsesInitImage(pods, releasedInitImage),
			"pre-upgrade workload does not use released init image %q", releasedInitImage)
	}) {
		t.FailNow()
	}

	if !t.Run("upgrade to current checkout", func(t *testing.T) {
		chartDir, err := setup.GetGroveChartDir()
		require.NoError(t, err, "locate current Grove chart")
		chartVersion, err := setup.GetGroveChartVersion(chartDir)
		require.NoError(t, err, "read current Grove chart version")
		_, err = setup.UpgradeHelmChart(&setup.HelmInstallConfig{
			RestConfig:   restConfig,
			ReleaseName:  releaseName,
			ChartRef:     chartDir,
			ChartVersion: chartVersion,
			Namespace:    operatorNamespace,
			Wait:         true,
			ResetValues:  true,
			Timeout:      6 * time.Minute,
			Values: map[string]any{
				"image": map[string]any{
					"repository": "grove-operator",
					"tag":        currentImageTag,
					"pullPolicy": "IfNotPresent",
				},
				"deployment": map[string]any{
					"env": []any{
						map[string]any{
							"name":  "GROVE_INIT_CONTAINER_IMAGE",
							"value": "grove-initc",
						},
					},
				},
				"crdInstaller": map[string]any{
					"enabled": true,
					"image": map[string]any{
						"repository": "grove-install-crds",
						"tag":        currentImageTag,
						"pullPolicy": "IfNotPresent",
					},
				},
			},
			HelmLoggerFunc: t.Logf,
		})
		require.NoError(t, err, "upgrade Grove chart to current checkout")

		assertDeployment(t, ctx, dynamicClient,
			"grove-operator:"+currentImageTag,
			"grove-install-crds:"+currentImageTag,
			"grove-initc",
		)
	}) {
		t.FailNow()
	}

	if !t.Run("reconcile pre-upgrade workload", func(t *testing.T) {
		patchPodCliqueSetEventually(t, ctx, dynamicClient, workloadName, []byte(`{"spec":{"replicas":2}}`))

		pods := waitForWorkload(t, ctx, dynamicClient, 2, 5*time.Minute)
		currentPodUIDs := make([]string, 0, len(pods.Items))
		for _, pod := range pods.Items {
			currentPodUIDs = append(currentPodUIDs, string(pod.GetUID()))
		}
		require.Subsetf(t, currentPodUIDs, originalPodUIDs,
			"pods created by %s were replaced during the operator upgrade", fromVersion)
		expectedInitImage := "grove-initc:" + currentImageTag
		require.Truef(t, podUsesInitImage(pods, expectedInitImage),
			"scaled workload does not contain a pod created with current init image %q", expectedInitImage)
	}) {
		t.FailNow()
	}
}

func findOperatorDir(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok, "locate upgrade E2E source file")
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
}

func requireCommands(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		_, err := exec.LookPath(name)
		require.NoErrorf(t, err, "required command %q was not found in PATH", name)
	}
}

func durationFromEnv(t *testing.T, name string, fallback time.Duration) time.Duration {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	require.NoErrorf(t, err, "parse %s=%q as duration", name, raw)
	return value
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func latestGitHubRelease(ctx context.Context, t *testing.T, token string) string {
	t.Helper()
	client := github.NewClient(nil)
	if token != "" {
		client = client.WithAuthToken(token)
	}

	release, _, err := client.Repositories.GetLatestRelease(ctx, githubOwner, githubRepository)
	require.NoError(t, err, "get latest Grove release from GitHub")
	tagName := strings.TrimSpace(release.GetTagName())
	require.NotEmpty(t, tagName, "latest Grove GitHub release did not contain tag_name")
	return tagName
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func newKubernetesClients(t *testing.T, kubeconfig []byte) (*rest.Config, dynamic.Interface, *k8sclient.Client) {
	t.Helper()
	config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	require.NoError(t, err, "build Kubernetes client config")
	config.UserAgent = "grove-upgrade-e2e"

	dynamicClient, err := dynamic.NewForConfig(config)
	require.NoError(t, err, "create dynamic Kubernetes client")
	diagnosticClient, err := k8sclient.New(config)
	require.NoError(t, err, "create E2E Kubernetes client")
	return config, dynamicClient, diagnosticClient
}

func (r *commandRunner) mustRun(ctx context.Context, name string, args ...string) string {
	r.t.Helper()
	output, err := r.run(ctx, "", name, args...)
	require.NoErrorf(r.t, err, "run %s %s\n%s", name, strings.Join(args, " "), output)
	return output
}

func (r *commandRunner) run(ctx context.Context, stdin, name string, args ...string) (string, error) {
	r.t.Helper()
	r.t.Logf("$ %s %s", name, strings.Join(args, " "))
	return r.runQuiet(ctx, stdin, name, args...)
}

func (r *commandRunner) runQuiet(ctx context.Context, stdin, name string, args ...string) (string, error) {
	r.t.Helper()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = r.workingDir
	cmd.Env = r.environment
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return output.String(), ctxErr
	}
	return output.String(), err
}

func applyManifestEventually(t *testing.T, ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, manifest string) {
	t.Helper()
	decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
	for {
		object := &unstructured.Unstructured{}
		if err := decoder.Decode(object); err != nil {
			if err == io.EOF {
				return
			}
			require.NoError(t, err, "decode compatibility workload")
		}
		if len(object.Object) == 0 {
			continue
		}
		applyObjectEventually(t, ctx, client, mapper, object, 2*time.Minute)
	}
}

func applyObjectEventually(t *testing.T, ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, object *unstructured.Unstructured, timeout time.Duration) {
	t.Helper()
	gvk := object.GroupVersionKind()
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	require.NoErrorf(t, err, "resolve REST mapping for %s", gvk)
	namespace := ""
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		namespace = object.GetNamespace()
		require.NotEmptyf(t, namespace, "%s %q is namespaced but metadata.namespace is empty", gvk.Kind, object.GetName())
	}
	payload, err := json.Marshal(object)
	require.NoErrorf(t, err, "encode %s %s/%s", object.GetKind(), namespace, object.GetName())

	pollCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		_, err := client.Resource(mapping.Resource).Namespace(namespace).Patch(pollCtx, object.GetName(), types.ApplyPatchType, payload, metav1.PatchOptions{
			FieldManager: "grove-upgrade-e2e",
			Force:        new(true),
		})
		assert.NoErrorf(collect, err, "apply %s %s/%s", object.GetKind(), namespace, object.GetName())
	}, timeout, 2*time.Second)
}

func patchPodCliqueSetEventually(t *testing.T, ctx context.Context, client dynamic.Interface, name string, patch []byte) {
	t.Helper()
	pollCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		_, err := client.Resource(podCliqueSetsGVR).Namespace(workloadNamespace).Patch(
			pollCtx, name, types.MergePatchType, patch, metav1.PatchOptions{},
		)
		assert.NoErrorf(collect, err, "patch PodCliqueSet %s/%s", workloadNamespace, name)
	}, 2*time.Minute, 2*time.Second)
}

func waitForWorkload(t *testing.T, ctx context.Context, client dynamic.Interface, expectedReplicas int64, timeout time.Duration) unstructured.UnstructuredList {
	t.Helper()
	pollCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var pods unstructured.UnstructuredList
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		pcs, err := client.Resource(podCliqueSetsGVR).Namespace(workloadNamespace).Get(
			pollCtx, workloadName, metav1.GetOptions{},
		)
		require.NoError(collect, err, "get PodCliqueSet")

		podList, err := client.Resource(podsGVR).Namespace(workloadNamespace).List(pollCtx, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/part-of=" + workloadName,
		})
		require.NoError(collect, err, "list workload pods")
		pods = *podList

		observedGeneration, found, err := unstructured.NestedInt64(pcs.Object, "status", "observedGeneration")
		require.NoError(collect, err, "read status.observedGeneration")
		require.True(collect, found, "status.observedGeneration is unset")
		assert.Equal(collect, pcs.GetGeneration(), observedGeneration, "observed generation")

		specReplicas, found, err := unstructured.NestedInt64(pcs.Object, "spec", "replicas")
		require.NoError(collect, err, "read spec.replicas")
		require.True(collect, found, "spec.replicas is unset")
		assert.Equal(collect, expectedReplicas, specReplicas, "spec.replicas")

		statusReplicas, found, err := unstructured.NestedInt64(pcs.Object, "status", "replicas")
		require.NoError(collect, err, "read status.replicas")
		require.True(collect, found, "status.replicas is unset")
		assert.Equal(collect, expectedReplicas, statusReplicas, "status.replicas")

		availableReplicas, found, err := unstructured.NestedInt64(pcs.Object, "status", "availableReplicas")
		require.NoError(collect, err, "read status.availableReplicas")
		require.True(collect, found, "status.availableReplicas is unset")
		assert.Equal(collect, expectedReplicas, availableReplicas, "status.availableReplicas")

		expectedPods := int(expectedReplicas) * 2 // compatibilityWorkload has two one-pod cliques per PCS replica.
		assert.Len(collect, pods.Items, expectedPods, "workload pods")
		for i := range pods.Items {
			assert.Truef(collect, podContainersReady(&pods.Items[i]), "pod %q containers are not ready", pods.Items[i].GetName())
		}
	}, timeout, 2*time.Second, "wait for workload to reach %d available replicas", expectedReplicas)
	return pods
}

func podContainersReady(pod *unstructured.Unstructured) bool {
	statuses, found, err := unstructured.NestedSlice(pod.Object, "status", "containerStatuses")
	if err != nil || !found || len(statuses) == 0 {
		return false
	}
	for _, rawStatus := range statuses {
		status, ok := rawStatus.(map[string]any)
		if !ok {
			return false
		}
		ready, found, err := unstructured.NestedBool(status, "ready")
		if err != nil || !found || !ready {
			return false
		}
	}
	return true
}

func assertDeployment(t *testing.T, ctx context.Context, client dynamic.Interface, operatorImage, crdInstallerImage, initRepository string) {
	t.Helper()
	deploy, err := client.Resource(deploymentsGVR).Namespace(operatorNamespace).Get(
		ctx, "grove-operator", metav1.GetOptions{},
	)
	require.NoError(t, err, "get Grove deployment")

	operator := findContainer(deploy.Object, "grove-operator", "spec", "template", "spec", "containers")
	require.Equal(t, operatorImage, imageOf(operator), "operator deployment image")
	if crdInstallerImage != "" {
		installer := findContainer(deploy.Object, "crd-installer", "spec", "template", "spec", "initContainers")
		require.Equal(t, crdInstallerImage, imageOf(installer), "CRD installer image")
	}
	if initRepository != "" {
		actual := containerEnvValue(operator, "GROVE_INIT_CONTAINER_IMAGE")
		require.Equal(t, initRepository, actual, "GROVE_INIT_CONTAINER_IMAGE")
	}
}

func findContainer(object map[string]any, name string, fields ...string) map[string]any {
	containers, found, err := unstructured.NestedSlice(object, fields...)
	if err != nil || !found {
		return nil
	}
	for _, rawContainer := range containers {
		container, ok := rawContainer.(map[string]any)
		if !ok {
			continue
		}
		containerName, found, err := unstructured.NestedString(container, "name")
		if err == nil && found && containerName == name {
			return container
		}
	}
	return nil
}

func imageOf(container map[string]any) string {
	image, _, _ := unstructured.NestedString(container, "image")
	return image
}

func containerEnvValue(container map[string]any, name string) string {
	environment, found, err := unstructured.NestedSlice(container, "env")
	if err != nil || !found {
		return ""
	}
	for _, rawVariable := range environment {
		variable, ok := rawVariable.(map[string]any)
		if !ok {
			continue
		}
		variableName, nameFound, nameErr := unstructured.NestedString(variable, "name")
		value, valueFound, valueErr := unstructured.NestedString(variable, "value")
		if nameErr == nil && valueErr == nil && nameFound && valueFound && variableName == name {
			return value
		}
	}
	return ""
}

func podUsesInitImage(pods unstructured.UnstructuredList, image string) bool {
	return slices.ContainsFunc(pods.Items, func(pod unstructured.Unstructured) bool {
		initContainer := findContainer(pod.Object, "grove-initc", "spec", "initContainers")
		return imageOf(initContainer) == image
	})
}

const compatibilityWorkload = `
apiVersion: v1
kind: Namespace
metadata:
  name: grove-upgrade-e2e
---
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: upgrade-survivor
  namespace: grove-upgrade-e2e
spec:
  replicas: 1
  template:
    cliqueStartupType: CliqueStartupTypeExplicit
    cliques:
      - name: bootstrap
        spec:
          roleName: bootstrap
          replicas: 1
          minAvailable: 1
          podSpec:
            containers:
              - name: bootstrap
                image: registry.k8s.io/pause:3.10
      - name: worker
        spec:
          roleName: worker
          startsAfter:
            - bootstrap
          replicas: 1
          minAvailable: 1
          podSpec:
            containers:
              - name: worker
                image: registry.k8s.io/pause:3.10
`
