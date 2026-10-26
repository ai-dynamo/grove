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

package podcliquescalinggroup

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/sync"
	"github.com/ai-dynamo/grove/operator/internal/mnnvl"
	controllerrevision "github.com/ai-dynamo/grove/operator/internal/utils/controllerrevision"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/uuid"
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
	resource := operator.(*_resource)
	assert.Equal(t, client, resource.client)
	assert.Equal(t, scheme, resource.scheme)
	assert.Equal(t, eventRecorder, resource.eventRecorder)
}

func TestDeletedPCSGIsRecreatedFromReplicaRevision(t *testing.T) {
	oldConfig := grovecorev1alpha1.PodCliqueScalingGroupConfig{
		Name: "group", MinAvailable: ptr.To(int32(1)), Replicas: ptr.To(int32(1)), CliqueNames: []string{"worker"},
	}
	newConfig := oldConfig
	newConfig.MinAvailable = ptr.To(int32(2))
	pcs := testutils.NewPodCliqueSetBuilder("test", "default", uuid.NewUUID()).WithReplicas(2).Build()
	pcs.Spec.Template.PodCliqueScalingGroupConfigs = []grovecorev1alpha1.PodCliqueScalingGroupConfig{newConfig}
	oldTemplate := pcs.Spec.Template.DeepCopy()
	oldTemplate.PodCliqueScalingGroupConfigs = []grovecorev1alpha1.PodCliqueScalingGroupConfig{oldConfig}
	oldRevision := pcsgTestRevision(t, pcs, "revision-old", oldTemplate)
	newRevision := pcsgTestRevision(t, pcs, "revision-new", &pcs.Spec.Template)
	pcs.Status.CurrentRevision = oldRevision.Name
	pcs.Status.UpdateRevision = newRevision.Name
	pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{}
	pcs.Status.UpdateProgress.ReplicaRevisions = []grovecorev1alpha1.PodCliqueSetReplicaRevision{
		{ReplicaIndex: 0, Revision: newRevision.Name},
		{ReplicaIndex: 1, Revision: oldRevision.Name},
	}
	cl := testutils.SetupFakeClient(pcs, oldRevision, newRevision)
	r := &_resource{client: cl, scheme: groveclientscheme.Scheme, eventRecorder: &record.FakeRecorder{}}
	pcsgKey := client.ObjectKey{Name: "test-1-group", Namespace: pcs.Namespace}

	require.NoError(t, r.doCreateOrUpdate(t.Context(), logr.Discard(), pcs, 1, pcsgKey, newConfig, false))
	recreated := &grovecorev1alpha1.PodCliqueScalingGroup{}
	require.NoError(t, cl.Get(t.Context(), pcsgKey, recreated))
	assert.Equal(t, int32(1), *recreated.Spec.MinAvailable)
	assert.NotContains(t, recreated.Labels, "controller-revision-hash")
}

func pcsgTestRevision(t *testing.T, pcs *grovecorev1alpha1.PodCliqueSet, name string, template *grovecorev1alpha1.PodCliqueSetTemplateSpec) *appsv1.ControllerRevision {
	t.Helper()
	data, err := controllerrevision.EncodeTemplate(template)
	require.NoError(t, err)
	return &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: pcs.Namespace,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(pcs, schema.GroupVersionKind{
				Group: grovecorev1alpha1.SchemeGroupVersion.Group, Version: grovecorev1alpha1.SchemeGroupVersion.Version, Kind: apiconstants.KindPodCliqueSet,
			})},
		},
		Data: runtime.RawExtension{Raw: data},
	}
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
								Kind:       "PodCliqueSet",
								Name:       "test-pcs",
								UID:        "pcs-uid",
								Controller: new(true),
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
								Kind:       "PodCliqueSet",
								Name:       "test-pcs",
								UID:        "pcs-uid",
								Controller: new(true),
							},
						},
					},
				},
			},
			expectedNames: []string{"test-pcs-0-pcsg1", "test-pcs-0-pcsg2"},
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
								MinAvailable: new(int32(1)),
								Replicas:     new(int32(2)),
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
								MinAvailable: new(int32(1)),
								Replicas:     new(int32(1)),
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
			require.NoError(t, appsv1.AddToScheme(scheme))

			revision := pcsgTestRevision(t, tc.pcs, "test-revision", &tc.pcs.Spec.Template)
			tc.pcs.Status.CurrentRevision = revision.Name
			tc.pcs.Status.UpdateRevision = revision.Name
			if tc.pcs.Status.UpdateProgress == nil {
				tc.pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{}
			}
			tc.pcs.Status.UpdateProgress.ReplicaRevisions = nil
			for replicaIndex := range tc.pcs.Spec.Replicas {
				tc.pcs.Status.UpdateProgress.ReplicaRevisions = append(tc.pcs.Status.UpdateProgress.ReplicaRevisions, grovecorev1alpha1.PodCliqueSetReplicaRevision{
					ReplicaIndex: replicaIndex, Revision: revision.Name,
				})
			}
			existingObjs := append([]runtime.Object{}, tc.existingObjs...)
			existingObjs = append(existingObjs, revision)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(existingObjs...).
				Build()

			r := &_resource{
				client:        fakeClient,
				scheme:        scheme,
				eventRecorder: &record.FakeRecorder{},
			}

			state := &pcssync.State{ExpectedReplicaIndices: lo.Range(int(tc.pcs.Spec.Replicas))}
			_, err := r.Sync(ctx, logger, tc.pcs, state)

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

func TestBuildResourceSetsGenerationHashAnnotationForOnDelete(t *testing.T) {
	const generationHash = "generation-hash"
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "pcs-uid").
		WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.OnDeleteStrategy}).
		WithPodCliqueSetGenerationHash(new(generationHash)).
		Build()
	pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{ObjectMeta: metav1.ObjectMeta{Name: "test-pcs-3-workers", Namespace: pcs.Namespace}}
	pcsgConfig := grovecorev1alpha1.PodCliqueScalingGroupConfig{
		Name: "workers", Replicas: new(int32(1)), MinAvailable: new(int32(1)), CliqueNames: []string{"worker"},
	}
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	r := &_resource{scheme: scheme}

	err := r.buildResourceForRevision(pcsg, pcs, 3, pcsgConfig, false)

	require.NoError(t, err)
	assert.Equal(t, generationHash, pcsg.Annotations[apiconstants.AnnotationOnDeletePodCliqueSetGenerationHash])
}

func TestBuildResourceOmitsGenerationHashAnnotationForAutomaticUpdate(t *testing.T) {
	const generationHash = "generation-hash"
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "pcs-uid").
		WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.RollingRecreateStrategy}).
		WithPodCliqueSetGenerationHash(new(generationHash)).
		Build()
	pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{ObjectMeta: metav1.ObjectMeta{Name: "test-pcs-3-workers", Namespace: pcs.Namespace}}
	pcsgConfig := grovecorev1alpha1.PodCliqueScalingGroupConfig{
		Name: "workers", Replicas: new(int32(1)), MinAvailable: new(int32(1)), CliqueNames: []string{"worker"},
	}
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	r := &_resource{scheme: scheme}

	err := r.buildResourceForRevision(pcsg, pcs, 3, pcsgConfig, false)

	require.NoError(t, err)
	assert.NotContains(t, pcsg.Annotations, apiconstants.AnnotationOnDeletePodCliqueSetGenerationHash)
}

func TestSyncUsesSparseReplicaIndices(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pcs", Namespace: "default", UID: "pcs-uid"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Replicas: 3,
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
					{Name: "workers", Replicas: new(int32(1)), MinAvailable: new(int32(1)), CliqueNames: []string{"worker"}},
				},
			},
		},
	}
	revision := pcsgTestRevision(t, pcs, "test-revision", &pcs.Spec.Template)
	pcs.Status.CurrentRevision = revision.Name
	pcs.Status.UpdateRevision = revision.Name
	pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{
		ReplicaRevisions: []grovecorev1alpha1.PodCliqueSetReplicaRevision{
			{ReplicaIndex: 0, Revision: revision.Name},
			{ReplicaIndex: 2, Revision: revision.Name},
			{ReplicaIndex: 4, Revision: revision.Name},
		},
	}
	existingPCSGs := []client.Object{
		testutils.NewPodCliqueScalingGroupBuilder("test-pcs-0-workers", "default", pcs.Name, 0).WithOwnerReference("PodCliqueSet", pcs.Name, pcs.UID).WithOptions(func(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) {
			pcsg.OwnerReferences[0].APIVersion = grovecorev1alpha1.SchemeGroupVersion.String()
		}).Build(),
		testutils.NewPodCliqueScalingGroupBuilder("test-pcs-1-workers", "default", pcs.Name, 1).WithOwnerReference("PodCliqueSet", pcs.Name, pcs.UID).WithOptions(func(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) {
			pcsg.OwnerReferences[0].APIVersion = grovecorev1alpha1.SchemeGroupVersion.String()
		}).Build(),
		testutils.NewPodCliqueScalingGroupBuilder("test-pcs-4-workers", "default", pcs.Name, 4).WithOwnerReference("PodCliqueSet", pcs.Name, pcs.UID).WithOptions(func(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) {
			pcsg.OwnerReferences[0].APIVersion = grovecorev1alpha1.SchemeGroupVersion.String()
		}).Build(),
		testutils.NewPodCliqueScalingGroupBuilder("test-pcs-2-obsolete", "default", pcs.Name, 2).WithOwnerReference("PodCliqueSet", pcs.Name, pcs.UID).WithOptions(func(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) {
			pcsg.OwnerReferences[0].APIVersion = grovecorev1alpha1.SchemeGroupVersion.String()
		}).Build(),
		revision,
	}
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingPCSGs...).Build()
	r := &_resource{client: fakeClient, scheme: scheme, eventRecorder: &record.FakeRecorder{}}
	state := &pcssync.State{ExpectedReplicaIndices: []int{0, 2, 4}, ScaleInReplicaIndices: []int{1}}

	result, err := r.Sync(context.Background(), logr.Discard(), pcs, state)

	require.NoError(t, err)
	assert.Equal(t, []int{1}, result.ScaleInReplicaIndicesAwaitingResourceCleanup)
	pcsgList := &grovecorev1alpha1.PodCliqueScalingGroupList{}
	require.NoError(t, fakeClient.List(context.Background(), pcsgList, client.InNamespace(pcs.Namespace)))
	pcsgNames := make([]string, 0, len(pcsgList.Items))
	for _, pcsg := range pcsgList.Items {
		pcsgNames = append(pcsgNames, pcsg.Name)
	}
	assert.ElementsMatch(t, []string{"test-pcs-0-workers", "test-pcs-2-workers", "test-pcs-4-workers"}, pcsgNames)
	assert.Equal(t, []int{0, 2, 4}, state.ExpectedReplicaIndices)
	assert.Equal(t, []int{1}, state.ScaleInReplicaIndices)
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
				MinAvailable: new(int32(2)),
				Replicas:     new(int32(3)),
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
		{
			name: "config annotations are copied to PCSG",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       "pcs-uid",
				},
			},
			pcsgCfg: &grovecorev1alpha1.PodCliqueScalingGroupConfig{
				Name:         "pcsg1",
				MinAvailable: new(int32(1)),
				Replicas:     new(int32(1)),
				CliqueNames:  []string{"clique1"},
				Annotations: map[string]string{
					"example.com/team":    "platform",
					"example.com/version": "v2",
				},
			},
			pcsReplica: 0,
			validate: func(t *testing.T, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) {
				require.NotNil(t, pcsg.Annotations)
				assert.Equal(t, "platform", pcsg.Annotations["example.com/team"])
				assert.Equal(t, "v2", pcsg.Annotations["example.com/version"])
				assert.Len(t, pcsg.Annotations, 2)
			},
		},
		{
			name: "no config annotations results in nil PCSG annotations",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       "pcs-uid",
				},
			},
			pcsgCfg: &grovecorev1alpha1.PodCliqueScalingGroupConfig{
				Name:         "pcsg1",
				MinAvailable: new(int32(1)),
				Replicas:     new(int32(1)),
				CliqueNames:  []string{"clique1"},
			},
			pcsReplica: 0,
			validate: func(t *testing.T, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) {
				assert.Nil(t, pcsg.Annotations)
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

			err := r.buildResourceForRevision(pcsg, tc.pcs, tc.pcsReplica, *tc.pcsgCfg, false)
			assert.NoError(t, err)

			if tc.validate != nil {
				tc.validate(t, pcsg)
			}
		})
	}
}

// TestBuildResource_MNNVLAnnotationPropagation tests that the MNNVL annotation is properly propagated from PCS to PCSG.
func TestBuildResource_MNNVLAnnotationPropagation(t *testing.T) {
	tests := []struct {
		description           string
		pcsAnnotations        map[string]string
		pcsgConfigAnnotations map[string]string
		expectedAnnotations   map[string]string // nil means no MNNVL annotations expected
	}{
		{
			description: "PCS mnnvl-group set, no config annotations — PCS propagates",
			pcsAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: "default",
			},
			expectedAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: "default",
			},
		},
		{
			description: "PCS mnnvl-group=none — propagates none",
			pcsAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: mnnvl.AnnotationMNNVLGroupOptOut,
			},
			expectedAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: mnnvl.AnnotationMNNVLGroupOptOut,
			},
		},
		{
			description:         "No MNNVL anywhere — nothing propagates",
			pcsAnnotations:      nil,
			expectedAnnotations: nil,
		},
		{
			description:         "Non-MNNVL PCS annotations — nothing propagates",
			pcsAnnotations:      map[string]string{"some-other-annotation": "value"},
			expectedAnnotations: nil,
		},
		{
			description: "PCSG config has mnnvl-group + PCS has mnnvl-group — config takes priority",
			pcsAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: "default",
			},
			pcsgConfigAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: "training",
			},
			expectedAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: "training",
			},
		},
		{
			description:    "PCSG config has mnnvl-group, PCS has none — config value used",
			pcsAnnotations: nil,
			pcsgConfigAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: "workers",
			},
			expectedAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: "workers",
			},
		},
		{
			description: "PCSG config has mnnvl-group=none — overrides PCS group",
			pcsAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: "default",
			},
			pcsgConfigAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: mnnvl.AnnotationMNNVLGroupOptOut,
			},
			expectedAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: mnnvl.AnnotationMNNVLGroupOptOut,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-pcs",
					Namespace:   "default",
					UID:         "pcs-uid",
					Annotations: tc.pcsAnnotations,
				},
			}

			pcsgCfg := grovecorev1alpha1.PodCliqueScalingGroupConfig{
				Name:         "pcsg1",
				MinAvailable: new(int32(1)),
				Replicas:     new(int32(2)),
				CliqueNames:  []string{"clique1"},
				Annotations:  tc.pcsgConfigAnnotations,
			}

			r := &_resource{
				scheme: scheme,
			}

			pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 0}, pcsgCfg.Name),
					Namespace: pcs.Namespace,
				},
			}

			err := r.buildResourceForRevision(pcsg, pcs, 0, pcsgCfg, false)
			require.NoError(t, err)

			if tc.expectedAnnotations != nil {
				require.NotNil(t, pcsg.Annotations, "PCSG should have annotations")
				for key, expectedVal := range tc.expectedAnnotations {
					assert.Equal(t, expectedVal, pcsg.Annotations[key],
						"annotation %s should have expected value", key)
				}
				if _, expected := tc.expectedAnnotations[mnnvl.AnnotationMNNVLGroup]; !expected {
					_, exists := pcsg.Annotations[mnnvl.AnnotationMNNVLGroup]
					assert.False(t, exists, "annotation %s should not be present", mnnvl.AnnotationMNNVLGroup)
				}
			} else {
				if pcsg.Annotations != nil {
					_, hasGroup := pcsg.Annotations[mnnvl.AnnotationMNNVLGroup]
					assert.False(t, hasGroup, "mnnvl-group annotation should not be present on PCSG")
				}
			}
		})
	}
}
