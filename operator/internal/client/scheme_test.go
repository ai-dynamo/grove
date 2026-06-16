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

	kaitopologyv1alpha1 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/kai/v1alpha1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	volcanov1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

func TestSchemeExcludesSchedulerSpecificAPIs(t *testing.T) {
	_, err := apiutil.GVKForObject(&corev1.Pod{}, Scheme)
	require.NoError(t, err)

	_, err = apiutil.GVKForObject(&kaitopologyv1alpha1.Topology{}, Scheme)
	require.Error(t, err)

	_, err = apiutil.GVKForObject(&volcanov1beta1.PodGroup{}, Scheme)
	require.Error(t, err)

	_, err = apiutil.GVKForObject(&apiextensionsv1.CustomResourceDefinition{}, Scheme)
	require.Error(t, err)
}
