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

package setup

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RequireWASAPIs skips tests when WAS APIs are absent, or fails when explicitly required.
func RequireWASAPIs(ctx context.Context, t *testing.T, cl client.Reader, namespace string) {
	t.Helper()
	for _, resource := range []client.ObjectList{
		&schedulingv1beta1.WorkloadList{},
		&schedulingv1beta1.PodGroupList{},
		&schedulingv1alpha3.CompositePodGroupList{},
	} {
		err := cl.List(ctx, resource, client.InNamespace(namespace), client.Limit(1))
		if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
			if os.Getenv("GROVE_E2E_REQUIRE_WAS") == "true" {
				t.Fatalf("required WAS API is unavailable: %v", err)
			}
			t.Skipf("WAS APIs unavailable: %v", err)
		}
		require.NoError(t, err, "probe %T API", resource)
	}
}
