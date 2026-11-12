//go:build e2e && e2eupgrade

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

package upgrade

import (
	"context"
	"fmt"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// workloadHashes records the grove.io/pod-template-hash label of a workload's pods and PodCliques.
type workloadHashes struct {
	// pods maps a pod UID to its hash label.
	pods map[types.UID]string
	// podCliques maps a PodClique name to its hash label.
	podCliques map[string]string
}

// captureWorkloadHashes records the hash labels of the workload's pods and PodCliques.
func captureWorkloadHashes(t *testing.T, tc *testctx.TestContext) workloadHashes {
	t.Helper()
	podList, err := tc.ListPods()
	require.NoError(t, err, "listing workload pods")
	h := workloadHashes{pods: map[types.UID]string{}, podCliques: map[string]string{}}
	for _, pod := range podList.Items {
		h.pods[pod.UID] = pod.Labels[apicommon.LabelPodTemplateHash]
	}
	for _, pclq := range listPodCliques(t, tc) {
		h.podCliques[pclq.Name] = pclq.Labels[apicommon.LabelPodTemplateHash]
	}
	return h
}

// requireUnchanged asserts that every recorded pod still exists and that no recorded hash label changed.
func (h workloadHashes) requireUnchanged(t *testing.T, tc *testctx.TestContext) {
	t.Helper()
	current := captureWorkloadHashes(t, tc)
	for uid, hash := range h.pods {
		currentHash, ok := current.pods[uid]
		require.Truef(t, ok, "pod %s was replaced", uid)
		require.Equalf(t, hash, currentHash, "hash label of pod %s changed", uid)
	}
	for name, hash := range h.podCliques {
		currentHash, ok := current.podCliques[name]
		require.Truef(t, ok, "PodClique %s was deleted", name)
		require.Equalf(t, hash, currentHash, "hash label of PodClique %s changed", name)
	}
}

// waitForRevisionsConverged waits until the workload's PodCliqueSet has an update revision that is also its
// current revision, and every PodClique and PodCliqueScalingGroup is labelled with and has reached it.
// It returns the PodCliqueSet.
func waitForRevisionsConverged(t *testing.T, tc *testctx.TestContext) *grovev1beta1.PodCliqueSet {
	t.Helper()
	pcsKey := types.NamespacedName{Namespace: tc.Workload.Namespace, Name: tc.Workload.Name}
	pcs := &grovev1beta1.PodCliqueSet{}
	var lastErr error
	err := wait.PollUntilContextTimeout(t.Context(), defaultPollInterval, defaultPollTimeout, true,
		func(ctx context.Context) (bool, error) {
			if err := tc.Client.Get(ctx, pcsKey, pcs); err != nil {
				return false, err
			}
			lastErr = revisionsConverged(ctx, tc, pcs)
			return lastErr == nil, nil
		})
	require.NoErrorf(t, err, "revisions did not converge: %v", lastErr)
	return pcs
}

// revisionsConverged returns why the revisions of pcs and its children have not converged, or nil.
func revisionsConverged(ctx context.Context, tc *testctx.TestContext, pcs *grovev1beta1.PodCliqueSet) error {
	rev := pcs.Status.UpdateRevision
	if rev == "" {
		return fmt.Errorf("PodCliqueSet %s has no update revision", pcs.Name)
	}
	if pcs.Status.CurrentRevision != rev {
		return fmt.Errorf("PodCliqueSet %s current revision %q is not update revision %q", pcs.Name, pcs.Status.CurrentRevision, rev)
	}
	inNamespace := client.InNamespace(pcs.Namespace)
	partOfPCS := client.MatchingLabels{apicommon.LabelPartOfKey: pcs.Name}

	pclqList := &grovev1beta1.PodCliqueList{}
	if err := tc.Client.List(ctx, pclqList, inNamespace, partOfPCS); err != nil {
		return err
	}
	if len(pclqList.Items) == 0 {
		return fmt.Errorf("PodCliqueSet %s has no PodCliques", pcs.Name)
	}
	for _, pclq := range pclqList.Items {
		if label := pclq.Labels[apicommon.LabelPodCliqueSetRevision]; label != rev {
			return fmt.Errorf("PodClique %s revision label is %q, want %q", pclq.Name, label, rev)
		}
		if pclq.Status.CurrentPodCliqueSetRevision != rev {
			return fmt.Errorf("PodClique %s reached revision %q, want %q", pclq.Name, pclq.Status.CurrentPodCliqueSetRevision, rev)
		}
	}

	pcsgList := &grovev1beta1.PodCliqueScalingGroupList{}
	if err := tc.Client.List(ctx, pcsgList, inNamespace, partOfPCS); err != nil {
		return err
	}
	for _, pcsg := range pcsgList.Items {
		if label := pcsg.Labels[apicommon.LabelPodCliqueSetRevision]; label != rev {
			return fmt.Errorf("PodCliqueScalingGroup %s revision label is %q, want %q", pcsg.Name, label, rev)
		}
		if pcsg.Status.CurrentPodCliqueSetRevision != rev {
			return fmt.Errorf("PodCliqueScalingGroup %s reached revision %q, want %q", pcsg.Name, pcsg.Status.CurrentPodCliqueSetRevision, rev)
		}
	}
	return nil
}

// listPodCliques lists the metadata of the workload's PodCliques. It reads grove.io/v1alpha1, which every
// release serves; v0.1.x releases do not serve v1beta1.
func listPodCliques(t *testing.T, tc *testctx.TestContext) []metav1.PartialObjectMetadata {
	t.Helper()
	pclqList := &metav1.PartialObjectMetadataList{}
	pclqList.SetGroupVersionKind(corev1alpha1.SchemeGroupVersion.WithKind("PodCliqueList"))
	require.NoError(t, tc.Client.List(t.Context(), pclqList,
		client.InNamespace(tc.Workload.Namespace),
		client.MatchingLabels{apicommon.LabelPartOfKey: tc.Workload.Name},
	), "listing workload PodCliques")
	return pclqList.Items
}
