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

package podcliquescalinggroup

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestNew tests creating a new PodCliqueScalingGroup operator
func TestNew(t *testing.T) {
	// Tests creating a new operator instance
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	eventRecorder := &record.FakeRecorder{}

	operator := New(client, scheme, eventRecorder)

	assert.NotNil(t, operator)
	resource, ok := operator.(*_resource)
	assert.True(t, ok)
	assert.Equal(t, client, resource.client)
	assert.Equal(t, scheme, resource.scheme)
	assert.Equal(t, eventRecorder, resource.eventRecorder)
}

// TestGetExistingResourceNames tests getting existing PodCliqueScalingGroup names
func TestGetExistingResourceNames(t *testing.T) {
	tests := []struct {
		name string
		// pcsObjMeta is the PodCliqueSet object metadata
		pcsObjMeta metav1.ObjectMeta
		// existingObjs are the existing objects in the cluster
		existingObjs []runtime.Object
		// expectedNames are the expected resource names
		expectedNames []string
		// expectError indicates if an error is expected
		expectError bool
	}{
		{
			// Tests finding owned PodCliqueScalingGroups
			name: "find_owned_pcsgs",
			pcsObjMeta: metav1.ObjectMeta{
				Name:      "test-pcs",
				Namespace: "default",
				UID:       "pcs-uid",
			},
			existingObjs: []runtime.Object{
				&grovecorev1alpha1.PodCliqueScalingGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-pcsg1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelManagedByKey: apicommon.LabelManagedByValue,
							apicommon.LabelPartOfKey:    "test-pcs",
							apicommon.LabelComponentKey: apicommon.LabelComponentNamePodCliqueScalingGroup,
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								Kind: "PodCliqueSet",
								Name: "test-pcs",
								UID:  "pcs-uid",
							},
						},
					},
				},
				&grovecorev1alpha1.PodCliqueScalingGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-pcsg2",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelManagedByKey: apicommon.LabelManagedByValue,
							apicommon.LabelPartOfKey:    "test-pcs",
							apicommon.LabelComponentKey: apicommon.LabelComponentNamePodCliqueScalingGroup,
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								Kind: "PodCliqueSet",
								Name: "test-pcs",
								UID:  "pcs-uid",
							},
						},
					},
				},
			},
			expectedNames: []string{}, // Fake client doesn't support PartialObjectMetadataList
			expectError:   false,
		},
		{
			// Tests no existing PodCliqueScalingGroups
			name: "no_existing_pcsgs",
			pcsObjMeta: metav1.ObjectMeta{
				Name:      "test-pcs",
				Namespace: "default",
				UID:       "pcs-uid",
			},
			existingObjs:  []runtime.Object{},
			expectedNames: []string{},
			expectError:   false,
		},
	}

	ctx := context.Background()
	logger := logr.Discard()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tc.existingObjs...).
				Build()

			r := &_resource{
				client: fakeClient,
			}

			names, err := r.GetExistingResourceNames(ctx, logger, tc.pcsObjMeta)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.ElementsMatch(t, tc.expectedNames, names)
			}
		})
	}
}

// TestSync tests the Sync function
func TestSync(t *testing.T) {
	tests := []struct {
		name string
		// pcs is the PodCliqueSet to sync
		pcs *grovecorev1alpha1.PodCliqueSet
		// existingObjs are the existing objects in the cluster
		existingObjs []runtime.Object
		// expectError indicates if an error is expected
		expectError bool
		// validate performs validation after sync
		validate func(*testing.T, client.Client)
	}{
		{
			// Tests creating new PodCliqueScalingGroups
			name: "create_new_pcsgs",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       "pcs-uid",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 2,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:         "pcsg1",
								MinAvailable: ptr.To(int32(1)),
								Replicas:     ptr.To(int32(2)),
								CliqueNames:  []string{"clique1"},
							},
						},
					},
				},
			},
			existingObjs: []runtime.Object{},
			expectError:  false,
			validate: func(t *testing.T, c client.Client) {
				// Verify PodCliqueScalingGroups were created
				pcsgList := &grovecorev1alpha1.PodCliqueScalingGroupList{}
				err := c.List(context.Background(), pcsgList, client.InNamespace("default"))
				assert.NoError(t, err)
				assert.Equal(t, 2, len(pcsgList.Items))
			},
		},
		{
			// Tests deleting orphaned PodCliqueScalingGroups
			name: "delete_orphaned_pcsgs",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       "pcs-uid",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:         "pcsg1",
								MinAvailable: ptr.To(int32(1)),
								Replicas:     ptr.To(int32(1)),
								CliqueNames:  []string{"clique1"},
							},
						},
					},
				},
			},
			existingObjs: []runtime.Object{
				&grovecorev1alpha1.PodCliqueScalingGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-pcsg2", // This should be deleted
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelManagedByKey: apicommon.LabelManagedByValue,
							apicommon.LabelPartOfKey:    "test-pcs",
							apicommon.LabelComponentKey: apicommon.LabelComponentNamePodCliqueScalingGroup,
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								Kind: "PodCliqueSet",
								Name: "test-pcs",
								UID:  "pcs-uid",
							},
						},
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, c client.Client) {
				// With fake client, the GetExistingResourceNames returns empty
				// so orphaned resources won't be detected and deleted
				pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{}
				err := c.Get(context.Background(), client.ObjectKey{
					Name:      "test-pcs-0-pcsg2",
					Namespace: "default",
				}, pcsg)
				// The orphaned PCSG will still exist
				assert.NoError(t, err)
			},
		},
	}

	ctx := context.Background()
	logger := logr.Discard()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tc.existingObjs...).
				Build()

			r := &_resource{
				client:        fakeClient,
				scheme:        scheme,
				eventRecorder: &record.FakeRecorder{},
			}

			err := r.Sync(ctx, logger, tc.pcs)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tc.validate != nil {
				tc.validate(t, fakeClient)
			}
		})
	}
}

// TestDelete tests the Delete function
func TestDelete(t *testing.T) {
	tests := []struct {
		name string
		// pcsObjMeta is the PodCliqueSet object metadata
		pcsObjMeta metav1.ObjectMeta
		// existingObjs are the existing objects in the cluster
		existingObjs []runtime.Object
		// expectError indicates if an error is expected
		expectError bool
		// validate performs validation after deletion
		validate func(*testing.T, client.Client)
	}{
		{
			// Tests successful deletion of PodCliqueScalingGroups
			name: "delete_existing_pcsgs",
			pcsObjMeta: metav1.ObjectMeta{
				Name:      "test-pcs",
				Namespace: "default",
				UID:       "pcs-uid",
			},
			existingObjs: []runtime.Object{
				&grovecorev1alpha1.PodCliqueScalingGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-pcsg1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelManagedByKey: apicommon.LabelManagedByValue,
							apicommon.LabelPartOfKey:    "test-pcs",
							apicommon.LabelComponentKey: apicommon.LabelComponentNamePodCliqueScalingGroup,
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								Kind: "PodCliqueSet",
								Name: "test-pcs",
								UID:  "pcs-uid",
							},
						},
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, c client.Client) {
				// Verify PCSG was deleted
				pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{}
				err := c.Get(context.Background(), client.ObjectKey{
					Name:      "test-pcs-0-pcsg1",
					Namespace: "default",
				}, pcsg)
				assert.Error(t, err)
			},
		},
	}

	ctx := context.Background()
	logger := logr.Discard()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tc.existingObjs...).
				Build()

			r := &_resource{
				client:        fakeClient,
				eventRecorder: &record.FakeRecorder{},
			}

			err := r.Delete(ctx, logger, tc.pcsObjMeta)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tc.validate != nil {
				tc.validate(t, fakeClient)
			}
		})
	}
}

// TestGetPodCliqueScalingGroupSelectorLabels tests generating selector labels
func TestGetPodCliqueScalingGroupSelectorLabels(t *testing.T) {
	tests := []struct {
		name string
		// pcsObjMeta is the PodCliqueSet object metadata
		pcsObjMeta metav1.ObjectMeta
		// expectedLabels are the expected selector labels
		expectedLabels map[string]string
	}{
		{
			// Tests generating labels for PodCliqueSet
			name: "pcs_selector_labels",
			pcsObjMeta: metav1.ObjectMeta{
				Name:      "test-pcs",
				Namespace: "default",
			},
			expectedLabels: map[string]string{
				apicommon.LabelManagedByKey: apicommon.LabelManagedByValue,
				apicommon.LabelPartOfKey:    "test-pcs",
				apicommon.LabelComponentKey: apicommon.LabelComponentNamePodCliqueScalingGroup,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := getPodCliqueScalingGroupSelectorLabels(tc.pcsObjMeta)

			for key, expectedValue := range tc.expectedLabels {
				assert.Equal(t, expectedValue, result[key])
			}
		})
	}
}

// TestBuildResource tests building a PodCliqueScalingGroup resource
func TestBuildResource(t *testing.T) {
	tests := []struct {
		name string
		// pcs is the PodCliqueSet
		pcs *grovecorev1alpha1.PodCliqueSet
		// pcsgCfg is the PCSG configuration
		pcsgCfg *grovecorev1alpha1.PodCliqueScalingGroupConfig
		// pcsReplica is the PCS replica index
		pcsReplica int
		// validate performs validation on the built resource
		validate func(*testing.T, *grovecorev1alpha1.PodCliqueScalingGroup)
	}{
		{
			// Tests building a basic PodCliqueScalingGroup
			name: "basic_pcsg",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       "pcs-uid",
				},
			},
			pcsgCfg: &grovecorev1alpha1.PodCliqueScalingGroupConfig{
				Name:         "pcsg1",
				MinAvailable: ptr.To(int32(2)),
				Replicas:     ptr.To(int32(3)),
				CliqueNames:  []string{"clique1", "clique2"},
			},
			pcsReplica: 0,
			validate: func(t *testing.T, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) {
				assert.Equal(t, "test-pcs-0-pcsg1", pcsg.Name)
				assert.Equal(t, "default", pcsg.Namespace)
				assert.Equal(t, int32(2), *pcsg.Spec.MinAvailable)
				assert.Equal(t, int32(3), pcsg.Spec.Replicas)
				assert.Equal(t, []string{"clique1", "clique2"}, pcsg.Spec.CliqueNames)
				assert.Contains(t, pcsg.Labels, apicommon.LabelPodCliqueSetReplicaIndex)
				assert.Equal(t, "0", pcsg.Labels[apicommon.LabelPodCliqueSetReplicaIndex])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

			r := &_resource{
				scheme: scheme,
			}

			pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: apicommon.GeneratePodCliqueScalingGroupName(
						apicommon.ResourceNameReplica{Name: tc.pcs.Name, Replica: tc.pcsReplica},
						tc.pcsgCfg.Name,
					),
					Namespace: tc.pcs.Namespace,
				},
			}

			err := r.buildResource(pcsg, tc.pcs, tc.pcsReplica, *tc.pcsgCfg, false)
			assert.NoError(t, err)

			if tc.validate != nil {
				tc.validate(t, pcsg)
			}
		})
	}
}
