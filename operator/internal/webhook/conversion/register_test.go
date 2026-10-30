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

package conversion

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// registeredHandler registers the conversion webhook with a fake manager and returns the handler served at
// WebhookPath, together with the converter registry.
func registeredHandler(t *testing.T) (http.Handler, *testutils.FakeManager) {
	t.Helper()
	cl := testutils.NewTestClientBuilder().Build()
	mgr := &testutils.FakeManager{
		Client:        cl,
		Scheme:        cl.Scheme(),
		Logger:        logr.Discard(),
		WebhookServer: webhook.NewServer(webhook.Options{}),
	}
	require.NoError(t, Register(mgr))
	return mgr.WebhookServer.WebhookMux(), mgr
}

// convert sends one object to the conversion webhook and returns the response.
func convert(t *testing.T, handler http.Handler, desiredAPIVersion string, obj runtime.Object) *apiextensionsv1.ConversionResponse {
	t.Helper()
	raw, err := json.Marshal(obj)
	require.NoError(t, err)
	review := apiextensionsv1.ConversionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: apiextensionsv1.SchemeGroupVersion.String(), Kind: "ConversionReview"},
		Request: &apiextensionsv1.ConversionRequest{
			UID:               types.UID("uid"),
			DesiredAPIVersion: desiredAPIVersion,
			Objects:           []runtime.RawExtension{{Raw: raw}},
		},
	}
	body, err := json.Marshal(review)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, WebhookPath, bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, recorder.Code)
	var response apiextensionsv1.ConversionReview
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.NotNil(t, response.Response)
	return response.Response
}

func TestRegisterServesConvertedKinds(t *testing.T) {
	_, mgr := registeredHandler(t)
	for _, groupKind := range ConvertedGroupKinds() {
		_, ok := mgr.GetConverterRegistry().GetConverter(groupKind)
		assert.True(t, ok, "expected a converter for %s", groupKind)
	}
	for _, kind := range []string{"PodClique", "PodCliqueScalingGroup", "PodGangMap", "ClusterTopologyBinding"} {
		_, ok := mgr.GetConverterRegistry().GetConverter(grovecorev1beta1.Kind(kind))
		assert.False(t, ok, "%s uses the None conversion strategy and must not have a converter", kind)
	}
}

func alphaPodCliqueSet(topologyConstraint *grovecorev1alpha1.TopologyConstraint) *grovecorev1alpha1.PodCliqueSet {
	return &grovecorev1alpha1.PodCliqueSet{
		TypeMeta:   metav1.TypeMeta{APIVersion: grovecorev1alpha1.SchemeGroupVersion.String(), Kind: "PodCliqueSet"},
		ObjectMeta: metav1.ObjectMeta{Name: "pcs", Namespace: "default"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Replicas: 2,
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				TopologyConstraint: topologyConstraint,
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{{
					Name: "worker",
					Spec: grovecorev1alpha1.PodCliqueSpec{RoleName: "worker", Replicas: 3, MinAvailable: ptr.To[int32](2)},
				}},
				PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{{
					Name: "sg", CliqueNames: []string{"worker"}, Replicas: ptr.To[int32](4), MinAvailable: ptr.To[int32](2),
				}},
			},
		},
	}
}

func TestConvertPodCliqueSetRoundTrip(t *testing.T) {
	handler, _ := registeredHandler(t)
	original := alphaPodCliqueSet(&grovecorev1alpha1.TopologyConstraint{
		TopologyName: "topo",
		Pack:         &grovecorev1alpha1.TopologyPackConstraint{RequiredDomain: grovecorev1alpha1.TopologyDomainRack},
	})

	toHub := convert(t, handler, grovecorev1beta1.SchemeGroupVersion.String(), original)
	require.Equal(t, metav1.StatusSuccess, toHub.Result.Status, toHub.Result.Message)
	require.Len(t, toHub.ConvertedObjects, 1)
	hub := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, json.Unmarshal(toHub.ConvertedObjects[0].Raw, hub))
	assert.Equal(t, grovecorev1beta1.SchemeGroupVersion.String(), hub.APIVersion)
	assert.Equal(t, ptr.To[int32](2), hub.Spec.Template.Cliques[0].Spec.MinAvailable)

	toSpoke := convert(t, handler, grovecorev1alpha1.SchemeGroupVersion.String(), hub)
	require.Equal(t, metav1.StatusSuccess, toSpoke.Result.Status, toSpoke.Result.Message)
	roundTripped := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, json.Unmarshal(toSpoke.ConvertedObjects[0].Raw, roundTripped))
	assert.Equal(t, original, roundTripped)
}

func TestConvertPodCliqueSetNormalizesPackDomain(t *testing.T) {
	handler, _ := registeredHandler(t)
	original := alphaPodCliqueSet(&grovecorev1alpha1.TopologyConstraint{TopologyName: "topo", PackDomain: grovecorev1alpha1.TopologyDomainRack})

	response := convert(t, handler, grovecorev1beta1.SchemeGroupVersion.String(), original)
	require.Equal(t, metav1.StatusSuccess, response.Result.Status, response.Result.Message)
	var converted map[string]any
	require.NoError(t, json.Unmarshal(response.ConvertedObjects[0].Raw, &converted))
	topologyConstraint := converted["spec"].(map[string]any)["template"].(map[string]any)["topologyConstraint"].(map[string]any)
	assert.NotContains(t, topologyConstraint, "packDomain")
	assert.Equal(t, map[string]any{"required": "rack"}, topologyConstraint["pack"])
}
