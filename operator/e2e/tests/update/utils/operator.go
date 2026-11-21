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
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
	kubeutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RestartOperator simulates a Grove operator crash by deleting its pod in the operator namespace and
// waiting for the Deployment to bring up a fresh Ready replica. It lets a test assert the operator resumes
// in-flight work from persisted state after a restart.
func RestartOperator(tc *testctx.TestContext) error {
	pm := pods.NewPodManager(tc.Client, tests.Logger)
	before, err := pm.List(tc.Ctx, setup.OperatorNamespace, "")
	if err != nil {
		return fmt.Errorf("failed to list operator pods in %s: %w", setup.OperatorNamespace, err)
	}
	oldPodUIDs := make(map[types.UID]struct{}, len(before.Items))
	for i := range before.Items {
		oldPodUIDs[before.Items[i].UID] = struct{}{}
		if err := tc.Client.Delete(tc.Ctx, &before.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete operator pod %s: %w", before.Items[i].Name, err)
		}
	}
	// Wait until exactly one operator pod, none of the deleted ones, is Ready.
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		current, err := pm.List(ctx, setup.OperatorNamespace, "")
		if err != nil {
			return false, nil //nolint:nilerr // Retry transient API read failures until the polling deadline.
		}
		if len(current.Items) != 1 {
			return false, nil
		}
		pod := &current.Items[0]
		if _, isOld := oldPodUIDs[pod.UID]; isOld {
			return false, nil
		}
		return kubeutils.IsPodReady(pod), nil
	})
}

const pcsUpgradeFixtureAdmissionCondition = "skip-scheduling-upgrade-fixture"

// ExemptPCSFromAdmissionForUpgradeFixture excludes the test PCS from defaulting and
// validation while the operator is stopped. It registers restoration for test cleanup.
// Other objects and existing admission conditions are unaffected.
func ExemptPCSFromAdmissionForUpgradeFixture(t *testing.T, tc *testctx.TestContext) {
	t.Helper()
	t.Cleanup(func() {
		RestorePCSAdmissionAfterUpgradeFixture(t, tc)
	})

	expression := fmt.Sprintf(
		"request.namespace != %q || request.name != %q",
		tc.Namespace, tc.Workload.Name,
	)
	for _, webhookConfig := range pcsWebhookConfigurations() {
		patch, err := json.Marshal(map[string]any{
			"webhooks": []map[string]any{{
				"name": webhookConfig.webhookName,
				"matchConditions": []map[string]string{{
					"name":       pcsUpgradeFixtureAdmissionCondition,
					"expression": expression,
				}},
			}},
		})
		require.NoError(t, err)
		require.NoError(t, tc.Client.Patch(
			tc.Ctx, webhookConfig.configuration,
			client.RawPatch(types.StrategicMergePatchType, patch),
		))
	}
}

// RestorePCSAdmissionAfterUpgradeFixture removes only the test admission exemption.
// It is safe to call explicitly and again during test cleanup. Restoration failures
// are reported without preventing restoration of the remaining webhook configuration.
func RestorePCSAdmissionAfterUpgradeFixture(t *testing.T, tc *testctx.TestContext) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tc.Ctx), tc.Timeout)
	defer cancel()

	for _, webhookConfig := range pcsWebhookConfigurations() {
		restorePatch, err := json.Marshal(map[string]any{
			"webhooks": []map[string]any{{
				"name": webhookConfig.webhookName,
				"matchConditions": []map[string]string{{
					"name":   pcsUpgradeFixtureAdmissionCondition,
					"$patch": "delete",
				}},
			}},
		})
		if !assert.NoError(t, err) {
			continue
		}
		assert.NoError(t, tc.Client.Patch(
			ctx, webhookConfig.configuration,
			client.RawPatch(types.StrategicMergePatchType, restorePatch),
		), "restore admission for %s", webhookConfig.webhookName)
	}
}

// pcsWebhookConfiguration identifies a webhook within its Kubernetes configuration.
type pcsWebhookConfiguration struct {
	configuration client.Object
	webhookName   string
}

// pcsWebhookConfigurations creates references to the PCS defaulting and validating
// webhook configurations. It performs no API calls.
func pcsWebhookConfigurations() []pcsWebhookConfiguration {
	return []pcsWebhookConfiguration{
		{
			configuration: &admissionregistrationv1.MutatingWebhookConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "podcliqueset-defaulting-webhook"},
			},
			webhookName: "pcs.defaulting.webhooks.grove.io",
		},
		{
			configuration: &admissionregistrationv1.ValidatingWebhookConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "podcliqueset-validating-webhook"},
			},
			webhookName: "pcs.validating.webhooks.grove.io",
		},
	}
}
