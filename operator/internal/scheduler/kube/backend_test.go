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

package kube

import (
	"testing"

	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/record"
)

func TestBackend_PreparePod(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	profile := configv1beta1.SchedulerProfile{Name: configv1beta1.SchedulerNameKube}
	b := New(cl, cl.Scheme(), recorder, profile)

	pod := testutils.NewPodBuilder("test-pod", "default").Build()

	require.NoError(t, b.PreparePod(pod))

	assert.Equal(t, string(configv1beta1.SchedulerNameKube), pod.Spec.SchedulerName)
}
