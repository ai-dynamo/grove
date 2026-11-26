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

//go:build e2e

package utils

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ScalePodCliqueInPCS scales all PodClique instances for a given clique name across all PCS replicas.
// Uses tc.Workload.Name as the PCS name.
//
// IMPORTANT: This function scales PodClique resources directly rather than modifying the PCS template.
//
// Why we can't scale via the PCS template:
// The PCS controller intentionally preserves existing PodClique replica counts to support HPA
// (Horizontal Pod Autoscaler) scaling. When a PodClique already exists, the controller does:
//
//	if pclqExists {
//	    currentPCLQReplicas := pclq.Spec.Replicas  // Preserve existing value
//	    pclq.Spec = pclqTemplateSpec.Spec          // Apply template
//	    pclq.Spec.Replicas = currentPCLQReplicas   // Restore preserved value
//	}
//
// This design prevents the PCS controller from fighting with HPA, which directly mutates
// PodClique.Spec.Replicas. Without this behavior, HPA scaling would be immediately reverted
// on the next PCS reconciliation.
//
// As a result, the PCS template's clique replicas value is only used during initial PodClique
// creation. Post-creation scaling must be done by:
//   - HPA (configured via ScaleConfig in the PCS template)
//   - Direct patching of PodClique resources (what this function does)
//
// See: internal/controller/podcliqueset/components/podclique/podclique.go buildResource()
func ScalePodCliqueInPCS(tc *testctx.TestContext, cliqueName string, replicas int32) error {
	pcsName := tc.Workload.Name

	// Get the PCS to find out how many replicas it has
	var pcs grovev1beta1.PodCliqueSet
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Name: pcsName, Namespace: tc.Namespace}, &pcs); err != nil {
		return fmt.Errorf("failed to get PodCliqueSet: %w", err)
	}

	// Verify the clique exists in the PCS template
	found := false
	for _, clique := range pcs.Spec.Template.Cliques {
		if clique.Name == cliqueName {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("clique %s not found in PodCliqueSet %s template", cliqueName, pcsName)
	}

	pclqs := &grovev1beta1.PodCliqueList{}
	if err := tc.Client.List(tc.Ctx, pclqs,
		client.InNamespace(tc.Namespace),
		client.MatchingLabels{apicommon.LabelPartOfKey: pcsName},
	); err != nil {
		return fmt.Errorf("failed to list standalone PodCliques for %s: %w", cliqueName, err)
	}
	pclqs.Items = slices.DeleteFunc(pclqs.Items, func(pclq grovev1beta1.PodClique) bool {
		_, belongsToPCSG := pclq.Labels[apicommon.LabelPodCliqueScalingGroup]
		return belongsToPCSG || !strings.HasSuffix(pclq.Name, "-"+cliqueName)
	})
	if len(pclqs.Items) != int(pcs.Spec.Replicas) {
		return fmt.Errorf("found %d standalone PodCliques for %s, want %d PCS replicas", len(pclqs.Items), cliqueName, pcs.Spec.Replicas)
	}

	for i := range pclqs.Items {
		pclqName := pclqs.Items[i].Name
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Patch the PodClique's replicas directly
			patch := map[string]interface{}{
				"spec": map[string]interface{}{
					"replicas": replicas,
				},
			}
			patchBytes, err := json.Marshal(patch)
			if err != nil {
				return fmt.Errorf("failed to marshal patch: %w", err)
			}

			pclq := grovev1beta1.PodClique{
				ObjectMeta: metav1.ObjectMeta{Name: pclqName, Namespace: tc.Namespace},
			}
			return tc.Client.Patch(tc.Ctx, &pclq, client.RawPatch(types.MergePatchType, patchBytes))
		}); err != nil {
			return fmt.Errorf("failed to scale PodClique %s: %w", pclqName, err)
		}
	}

	return nil
}

// UpdatePCSUpdateStrategy changes the update strategy type of the PodCliqueSet.
// Uses tc.Workload.Name as the PCS name.
func UpdatePCSUpdateStrategy(tc *testctx.TestContext, strategyType grovev1beta1.UpdateStrategyType) error {
	tc.T.Helper()

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var pcs grovev1beta1.PodCliqueSet
		if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Name: tc.Workload.Name, Namespace: tc.Namespace}, &pcs); err != nil {
			return fmt.Errorf("failed to fetch PodCliqueSet: %w", err)
		}
		pcs.SetGroupVersionKind(grovev1beta1.SchemeGroupVersion.WithKind("PodCliqueSet"))
		pcs.SetManagedFields(nil)
		pcs.SetResourceVersion("")

		if pcs.Spec.UpdateStrategy == nil {
			pcs.Spec.UpdateStrategy = &grovev1beta1.PodCliqueSetUpdateStrategy{}
		}
		pcs.Spec.UpdateStrategy.Type = strategyType

		// Switching to OnDelete requires removing any RollingUpdate Configuration: the validating webhook
		// rejects a RollingUpdate that is set under OnDelete, and defaulting no longer clears it. This
		// mirrors what a consumer must do when changing the strategy.
		if strategyType == grovev1beta1.OnDeleteStrategy {
			for i := range pcs.Spec.Template.Cliques {
				pcs.Spec.Template.Cliques[i].RollingUpdate = nil
			}
			for i := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
				pcs.Spec.Template.PodCliqueScalingGroupConfigs[i].RollingUpdate = nil
			}
		}

		return tc.Client.Patch(tc.Ctx, &pcs, client.Apply, client.FieldOwner("e2e-rolling-update-test"), client.ForceOwnership)
	})
}

// TriggerPodCliqueUpdate triggers an update by adding/updating an environment variable in each named PodClique,
// all in one PodCliqueSet patch. Uses tc.Workload.Name as the PCS name.
func TriggerPodCliqueUpdate(tc *testctx.TestContext, cliqueNames ...string) error {
	return TriggerPodCliqueUpdateWithRevision(tc, fmt.Sprintf("%d", time.Now().UnixNano()), cliqueNames...)
}

// TriggerPodCliqueUpdateWithRevision triggers an update by setting UPDATE_TRIGGER to an explicit value
// in each named PodClique template. Explicit values let revision-transition tests reapply an earlier,
// byte-identical PCS template.
func TriggerPodCliqueUpdateWithRevision(tc *testctx.TestContext, updateValue string, cliqueNames ...string) error {
	pcsName := tc.Workload.Name

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var pcs grovev1beta1.PodCliqueSet
		if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Name: pcsName, Namespace: tc.Namespace}, &pcs); err != nil {
			return fmt.Errorf("failed to get PodCliqueSet: %w", err)
		}
		pcs.SetGroupVersionKind(grovev1beta1.SchemeGroupVersion.WithKind("PodCliqueSet"))
		pcs.SetManagedFields(nil)
		pcs.SetResourceVersion("")

		for _, cliqueName := range cliqueNames {
			found := false
			for i, clique := range pcs.Spec.Template.Cliques {
				if clique.Name == cliqueName {
					if len(clique.Spec.PodSpec.Containers) > 0 {
						container := &pcs.Spec.Template.Cliques[i].Spec.PodSpec.Containers[0]
						envVarFound := false
						for j := range container.Env {
							if container.Env[j].Name == "UPDATE_TRIGGER" {
								container.Env[j].Value = updateValue
								envVarFound = true
								break
							}
						}
						if !envVarFound {
							container.Env = append(container.Env, corev1.EnvVar{
								Name:  "UPDATE_TRIGGER",
								Value: updateValue,
							})
						}
						found = true
					}
					break
				}
			}

			if !found {
				return fmt.Errorf("clique %s not found in PodCliqueSet %s", cliqueName, pcsName)
			}
		}

		return tc.Client.Patch(tc.Ctx, &pcs, client.Apply, client.FieldOwner("e2e-rolling-update-test"), client.ForceOwnership)
	})
}
