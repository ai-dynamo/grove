// /*
// Copyright 2025 The Grove Authors.
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

//go:build e2e

package tests

import (
	"context"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/utils"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var crdGVR = schema.GroupVersionResource{
	Group:    "apiextensions.k8s.io",
	Version:  "v1",
	Resource: "customresourcedefinitions",
}

// groveCRDNames is the authoritative list of all CRDs that the crd-installer init container must apply.
var groveCRDNames = []string{
	"podcliques.grove.io",
	"podcliquesets.grove.io",
	"podcliquescalinggroups.grove.io",
	"clustertopologies.grove.io",
	"podgangs.scheduler.grove.io",
}

// Test_CRD_Installer_AllCRDsExist verifies that all 5 Grove CRDs are present and
// established in the cluster after the operator has been deployed.
func Test_CRD_Installer_AllCRDsExist(t *testing.T) {
	ctx := context.Background()
	sharedCluster := setup.SharedCluster(logger)
	_, _, dynamicClient := sharedCluster.GetClients()

	for _, crdName := range groveCRDNames {
		crd, err := dynamicClient.Resource(crdGVR).Get(ctx, crdName, metav1.GetOptions{})
		if err != nil {
			t.Errorf("CRD %q not found: %v", crdName, err)
			continue
		}
		// Verify the CRD is established (API server is serving it).
		conditions, found, err := utils.GetNestedSlice(crd.Object, "status", "conditions")
		if err != nil || !found {
			t.Errorf("CRD %q has no status conditions", crdName)
			continue
		}
		established := false
		for _, c := range conditions {
			cond, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			if cond["type"] == "Established" && cond["status"] == "True" {
				established = true
				break
			}
		}
		if !established {
			t.Errorf("CRD %q exists but is not Established", crdName)
		}
	}
}

// Test_CRD_Installer_InitContainerCompleted verifies that the crd-installer init container
// in the operator Pod ran to successful completion (exit code 0).
func Test_CRD_Installer_InitContainerCompleted(t *testing.T) {
	ctx := context.Background()
	sharedCluster := setup.SharedCluster(logger)
	clientset, _, _ := sharedCluster.GetClients()

	pods, err := clientset.CoreV1().Pods(setup.OperatorNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=grove-operator",
	})
	if err != nil {
		t.Fatalf("failed to list operator pods: %v", err)
	}
	if len(pods.Items) == 0 {
		t.Fatalf("no operator pods found in namespace %s", setup.OperatorNamespace)
	}

	pod := pods.Items[0]
	var crdInstallerStatus *v1.ContainerStatus
	for i := range pod.Status.InitContainerStatuses {
		if pod.Status.InitContainerStatuses[i].Name == "crd-installer" {
			crdInstallerStatus = &pod.Status.InitContainerStatuses[i]
			break
		}
	}

	if crdInstallerStatus == nil {
		t.Fatalf("crd-installer init container not found in pod %s; init containers present: %v",
			pod.Name, utils.InitContainerNames(pod))
	}
	if crdInstallerStatus.State.Terminated == nil {
		t.Fatalf("crd-installer init container in pod %s is not in Terminated state: %+v",
			pod.Name, crdInstallerStatus.State)
	}
	if crdInstallerStatus.State.Terminated.ExitCode != 0 {
		t.Errorf("crd-installer init container in pod %s exited with code %d (expected 0)",
			pod.Name, crdInstallerStatus.State.Terminated.ExitCode)
	}
}

// Test_CRD_Installer_Idempotent verifies that restarting the operator Pod (which re-runs
// the crd-installer init container) does not corrupt or remove existing CRDs.
func Test_CRD_Installer_Idempotent(t *testing.T) {
	ctx := context.Background()
	sharedCluster := setup.SharedCluster(logger)
	clientset, restConfig, dynamicClient := sharedCluster.GetClients()

	// Get the current operator pod name.
	pods, err := clientset.CoreV1().Pods(setup.OperatorNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=grove-operator",
	})
	if err != nil || len(pods.Items) == 0 {
		t.Fatalf("failed to get operator pod: %v (count: %d)", err, len(pods.Items))
	}
	podName := pods.Items[0].Name

	// Delete the pod to force a restart (Deployment will recreate it).
	if err := clientset.CoreV1().Pods(setup.OperatorNamespace).Delete(ctx, podName, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("failed to delete operator pod %s: %v", podName, err)
	}
	logger.Infof("deleted operator pod %s, waiting for replacement to be ready", podName)

	// Wait for a new, ready operator pod to appear.
	if err := utils.WaitForPodsInNamespace(ctx, setup.OperatorNamespace, restConfig, 1, 3*time.Minute, 5*time.Second, logger); err != nil {
		t.Fatalf("operator pod did not become ready after restart: %v", err)
	}

	// All 5 CRDs must still exist and be Established after the restart.
	for _, crdName := range groveCRDNames {
		_, err := dynamicClient.Resource(crdGVR).Get(ctx, crdName, metav1.GetOptions{})
		if err != nil {
			t.Errorf("CRD %q missing after operator pod restart: %v", crdName, err)
		}
	}
}
