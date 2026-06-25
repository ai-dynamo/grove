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

package client

import (
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	schedv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	kaitopologyv1alpha1 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/kai/v1alpha1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	volcanov1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

func TestSchemeIncludesSharedAPIs(t *testing.T) {
	_, err := apiutil.GVKForObject(&corev1.Pod{}, Scheme)
	require.NoError(t, err)

	_, err = apiutil.GVKForObject(&grovecorev1alpha1.PodCliqueSet{}, Scheme)
	require.NoError(t, err)

	_, err = apiutil.GVKForObject(&schedv1alpha1.PodGang{}, Scheme)
	require.NoError(t, err)
}

func TestSchemeDoesNotIncludeSchedulerSpecificAPIs(t *testing.T) {
	for _, obj := range []struct {
		name   string
		object runtime.Object
	}{
		{name: "KAI Topology", object: &kaitopologyv1alpha1.Topology{}},
		{name: "Volcano PodGroup", object: &volcanov1beta1.PodGroup{}},
	} {
		t.Run(obj.name, func(t *testing.T) {
			_, err := apiutil.GVKForObject(obj.object, Scheme)
			require.Error(t, err)
		})
	}
}
