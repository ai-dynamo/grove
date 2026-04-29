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

package computedomain

import (
	"context"
	"errors"
	"strconv"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/mnnvl"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testPCSName      = "test-pcs"
	testPCSNamespace = "test-ns"
)

// testScheme is a scheme that includes fakeComputeDomain types for testing.
// This is needed because the fake client requires the GVK to be registered.
var testScheme = func() *runtime.Scheme {
	s := runtime.NewScheme()
	// Add Grove scheme
	_ = grovecorev1alpha1.AddToScheme(s)
	// Add core scheme
	_ = corev1.AddToScheme(s)
	// Register ComputeDomain types for unstructured List to work with fake client
	s.AddKnownTypeWithName(mnnvl.ComputeDomainGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: mnnvl.ComputeDomainGVK.Group, Version: mnnvl.ComputeDomainGVK.Version, Kind: mnnvl.ComputeDomainGVK.Kind + "List"},
		&unstructured.UnstructuredList{},
	)
	return s
}()

// ================================
// Constructor Test
// ================================

func TestNew(t *testing.T) {
	cl := createTestClient()
	operator := New(cl, testScheme, record.NewFakeRecorder(10))
	assert.NotNil(t, operator)
}

// ================================
// Helper Function Tests
// ================================

func Test_generateComputeDomainName(t *testing.T) {
	testCases := []struct {
		description  string
		pcsName      string
		replicaIndex int
		groupName    string
		expected     string
	}{
		{
			description:  "default group replica 0",
			pcsName:      "mypcs",
			replicaIndex: 0,
			expected:     "mypcs-0",
		},
		{
			description:  "default group replica 5",
			pcsName:      "mypcs",
			replicaIndex: 5,
			expected:     "mypcs-5",
		},
		{
			description:  "default group different pcs name",
			pcsName:      "other-pcs",
			replicaIndex: 3,
			expected:     "other-pcs-3",
		},
		{
			description:  "named group",
			pcsName:      "mypcs",
			replicaIndex: 0,
			groupName:    "workers",
			expected:     "mypcs-0-workers",
		},
		{
			description:  "named group higher replica",
			pcsName:      "training",
			replicaIndex: 2,
			groupName:    "encoders",
			expected:     "training-2-encoders",
		},
	}

	t.Parallel()
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()
			result := generateComputeDomainName(tc.pcsName, tc.replicaIndex, tc.groupName)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestGetRequiredCDNames(t *testing.T) {
	testCases := []struct {
		description    string
		pcs            *grovecorev1alpha1.PodCliqueSet
		expectedGroups map[string]struct{}
	}{
		{
			description: "auto-mnnvl enabled, 2 replicas — default group",
			pcs:         createPCSWithMNNVLEnabled(2),
			expectedGroups: map[string]struct{}{
				"": {},
			},
		},
		{
			description: "mnnvl-group on PCS, 2 replicas — named group",
			pcs: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createPCSWithGPU(2)
				pcs.Annotations = map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}
				return pcs
			}(),
			expectedGroups: map[string]struct{}{
				"workers": {},
			},
		},
		{
			description: "clique-level groups — two distinct GPU groups",
			pcs: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createPCSWithGPU(1)
				pcs.Spec.Template.Cliques = append(pcs.Spec.Template.Cliques, &grovecorev1alpha1.PodCliqueTemplateSpec{
					Name:        "clique2",
					Annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "encoders"},
					Spec: grovecorev1alpha1.PodCliqueSpec{
						PodSpec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:  "encoder",
								Image: "alpine",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{constants.GPUResourceName: resource.MustParse("1")},
								},
							}},
						},
					},
				})
				pcs.Spec.Template.Cliques[0].Annotations = map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}
				return pcs
			}(),
			expectedGroups: map[string]struct{}{
				"workers":  {},
				"encoders": {},
			},
		},
		{
			description: "PCSG config group propagates to clique",
			pcs: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createPCSWithGPU(1)
				pcs.Spec.Template.PodCliqueScalingGroupConfigs = []grovecorev1alpha1.PodCliqueScalingGroupConfig{
					{
						Name:        "sg1",
						CliqueNames: []string{"clique1"},
						Annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "training"},
					},
				}
				return pcs
			}(),
			expectedGroups: map[string]struct{}{
				"training": {},
			},
		},
		{
			description: "PCS default group + clique named group — both collected",
			pcs: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createPCSWithMNNVLEnabled(2)
				pcs.Spec.Template.Cliques[0].Annotations = map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}
				pcs.Spec.Template.Cliques = append(pcs.Spec.Template.Cliques, &grovecorev1alpha1.PodCliqueTemplateSpec{
					Name: "clique2",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						PodSpec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:  "gpu2",
								Image: "alpine",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{constants.GPUResourceName: resource.MustParse("1")},
								},
							}},
						},
					},
				})
				return pcs
			}(),
			expectedGroups: map[string]struct{}{
				"":        {},
				"workers": {},
			},
		},
		{
			description:    "no MNNVL annotations — empty list",
			pcs:            createPCSWithGPU(2),
			expectedGroups: nil,
		},
		{
			description:    "auto-mnnvl disabled — empty list",
			pcs:            createPCSWithMNNVLDisabled(),
			expectedGroups: nil,
		},
		{
			description: "PCS auto-mnnvl enabled but only CPU cliques — no orphaned CD",
			pcs: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createPCSWithoutGPU()
				pcs.Annotations = map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLEnabled}
				return pcs
			}(),
			expectedGroups: nil,
		},
	}

	t.Parallel()
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()
			result := getRequiredCDNames(tc.pcs)
			if tc.expectedGroups == nil {
				assert.Nil(t, result)
				return
			}
			actualGroups := make(map[string]struct{})
			for _, cd := range result {
				actualGroups[cd.groupName] = struct{}{}
			}
			assert.Equal(t, tc.expectedGroups, actualGroups)

			expectedCount := len(tc.expectedGroups) * int(tc.pcs.Spec.Replicas)
			assert.Len(t, result, expectedCount, "should have one CD per group per replica")
		})
	}
}

func TestTriageCDs(t *testing.T) {
	testCases := []struct {
		description      string
		requiredCDs      []cdNameInfo
		existingCDFQNs   []string
		expectedToCreate []cdNameInfo
		expectedToDelete []string
	}{
		{
			description: "exact match — nothing to create or delete",
			requiredCDs: []cdNameInfo{
				{pcsName: "pcs", replicaIndex: 0},
				{pcsName: "pcs", replicaIndex: 1},
			},
			existingCDFQNs:   []string{"pcs-0", "pcs-1"},
			expectedToCreate: nil,
			expectedToDelete: nil,
		},
		{
			description: "scale out — create missing",
			requiredCDs: []cdNameInfo{
				{pcsName: "pcs", replicaIndex: 0},
				{pcsName: "pcs", replicaIndex: 1},
				{pcsName: "pcs", replicaIndex: 2},
			},
			existingCDFQNs: []string{"pcs-0"},
			expectedToCreate: []cdNameInfo{
				{pcsName: "pcs", replicaIndex: 1},
				{pcsName: "pcs", replicaIndex: 2},
			},
			expectedToDelete: nil,
		},
		{
			description: "scale in — delete excess",
			requiredCDs: []cdNameInfo{
				{pcsName: "pcs", replicaIndex: 0},
			},
			existingCDFQNs:   []string{"pcs-0", "pcs-1", "pcs-2"},
			expectedToCreate: nil,
			expectedToDelete: []string{"pcs-1", "pcs-2"},
		},
		{
			description: "group rename — delete old, create new",
			requiredCDs: []cdNameInfo{
				{pcsName: "pcs", replicaIndex: 0, groupName: "encoders"},
				{pcsName: "pcs", replicaIndex: 1, groupName: "encoders"},
			},
			existingCDFQNs: []string{"pcs-0-workers", "pcs-1-workers"},
			expectedToCreate: []cdNameInfo{
				{pcsName: "pcs", replicaIndex: 0, groupName: "encoders"},
				{pcsName: "pcs", replicaIndex: 1, groupName: "encoders"},
			},
			expectedToDelete: []string{"pcs-0-workers", "pcs-1-workers"},
		},
		{
			description: "all new — nothing existing",
			requiredCDs: []cdNameInfo{
				{pcsName: "pcs", replicaIndex: 0},
			},
			existingCDFQNs: nil,
			expectedToCreate: []cdNameInfo{
				{pcsName: "pcs", replicaIndex: 0},
			},
			expectedToDelete: nil,
		},
		{
			description:      "all excess — nothing required",
			requiredCDs:      nil,
			existingCDFQNs:   []string{"pcs-0", "pcs-1"},
			expectedToCreate: nil,
			expectedToDelete: []string{"pcs-0", "pcs-1"},
		},
	}

	t.Parallel()
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()
			toCreate, toDelete := triageCDs(tc.requiredCDs, tc.existingCDFQNs)
			if tc.expectedToCreate == nil {
				assert.Empty(t, toCreate)
			} else {
				assert.Equal(t, tc.expectedToCreate, toCreate)
			}
			assert.ElementsMatch(t, tc.expectedToDelete, toDelete)
		})
	}
}

func TestGetSelectorLabels(t *testing.T) {
	pcsName := "test-pcs"
	labels := getSelectorLabels(pcsName)

	assert.Equal(t, apicommon.LabelManagedByValue, labels[apicommon.LabelManagedByKey])
	assert.Equal(t, pcsName, labels[apicommon.LabelPartOfKey])
	assert.Equal(t, labelComponentNameComputeDomain, labels[apicommon.LabelComponentKey])
}

func TestEmptyComputeDomain(t *testing.T) {
	objKey := client.ObjectKey{Name: "test-cd", Namespace: "test-ns"}
	cd := emptyComputeDomain(objKey)

	assert.NotNil(t, cd)
	assert.Equal(t, "test-cd", cd.GetName())
	assert.Equal(t, "test-ns", cd.GetNamespace())
	assert.Equal(t, mnnvl.ComputeDomainGVK, cd.GroupVersionKind())
}

// ================================
// Sync Tests
// ================================

// TestSyncSkipsWhenMNNVLNotEnabled tests that Sync returns early when PCS doesn't have MNNVL enabled.
// We use a client that would fail on List - if Sync skips properly, it won't call List and won't error.
func TestSyncSkipsWhenMNNVLNotEnabled(t *testing.T) {
	testCases := []struct {
		description string
		pcs         *grovecorev1alpha1.PodCliqueSet
	}{
		{
			description: "no annotation",
			pcs:         createPCSWithGPU(1),
		},
		{
			description: "annotation set to false",
			pcs:         createPCSWithMNNVLDisabled(),
		},
		{
			description: "no GPU and no annotation",
			pcs:         createPCSWithoutGPU(),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			// Create a client that fails on List - proves we skipped before listing
			cl := createClientThatFailsOnList()
			operator := New(cl, testScheme, record.NewFakeRecorder(10))

			err := operator.Sync(context.Background(), logr.Discard(), tc.pcs)

			// If Sync skipped properly, it didn't call List, so no error
			assert.NoError(t, err)
		})
	}
}

// TestSyncCreatesComputeDomains tests that Sync creates ComputeDomains for each replica.
func TestSyncCreatesComputeDomains(t *testing.T) {
	testCases := []struct {
		description     string
		replicas        int32
		expectedCDNames []string
	}{
		{
			description:     "single replica",
			replicas:        1,
			expectedCDNames: []string{"test-pcs-0"},
		},
		{
			description:     "multiple replicas",
			replicas:        3,
			expectedCDNames: []string{"test-pcs-0", "test-pcs-1", "test-pcs-2"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			pcs := createPCSWithMNNVLEnabled(tc.replicas)
			cl := createTestClient()
			operator := New(cl, testScheme, record.NewFakeRecorder(10))

			err := operator.Sync(context.Background(), logr.Discard(), pcs)

			require.NoError(t, err)

			// Verify CDs were created
			for _, cdName := range tc.expectedCDNames {
				cd := emptyComputeDomain(client.ObjectKey{Name: cdName, Namespace: testPCSNamespace})
				err := cl.Get(context.Background(), client.ObjectKeyFromObject(cd), cd)
				assert.NoError(t, err, "CD %s should exist", cdName)

				// Verify CD has correct labels
				assert.Equal(t, apicommon.LabelManagedByValue, cd.GetLabels()[apicommon.LabelManagedByKey])
				assert.Equal(t, testPCSName, cd.GetLabels()[apicommon.LabelPartOfKey])
				assert.Equal(t, labelComponentNameComputeDomain, cd.GetLabels()[apicommon.LabelComponentKey])
				_, hasGroupLabel := cd.GetLabels()[mnnvl.LabelMNNVLGroup]
				assert.False(t, hasGroupLabel, "default group CD should not have mnnvl-group label")

				// Verify CD has finalizer
				assert.Contains(t, cd.GetFinalizers(), mnnvl.FinalizerComputeDomain)

				// Verify CD has RCT reference in spec
				rctName, found, err := unstructured.NestedString(cd.Object, "spec", "channel", "resourceClaimTemplate", "name")
				assert.NoError(t, err)
				assert.True(t, found, "RCT reference should be set")
				assert.Equal(t, cdName, rctName, "RCT name should match CD name")

				// Verify numNodes is set to 0 (elastic mode)
				numNodes, found, err := unstructured.NestedInt64(cd.Object, "spec", "numNodes")
				assert.NoError(t, err)
				assert.True(t, found, "numNodes should be set")
				assert.Equal(t, int64(0), numNodes, "numNodes should be 0 for elastic mode")
			}
		})
	}
}

// TestSyncScaleIn tests that Sync deletes excess ComputeDomains when scaling down.
func TestSyncScaleIn(t *testing.T) {
	// Setup: 4 existing CDs, scale down to 2 replicas
	pcs := createPCSWithMNNVLEnabled(2)
	existingCDs := createTestCDs(testPCSName, testPCSNamespace, 4)
	cl := createTestClientWithCDs(existingCDs)
	operator := New(cl, testScheme, record.NewFakeRecorder(10))

	err := operator.Sync(context.Background(), logr.Discard(), pcs)

	require.NoError(t, err)

	// Verify CDs 0 and 1 still exist
	for i := 0; i < 2; i++ {
		cdName := testPCSName + "-" + strconv.Itoa(i)
		cd := emptyComputeDomain(client.ObjectKey{Name: cdName, Namespace: testPCSNamespace})
		err := cl.Get(context.Background(), client.ObjectKeyFromObject(cd), cd)
		assert.NoError(t, err, "CD %s should still exist", cdName)
	}

	// Verify CDs 2 and 3 were deleted
	for i := 2; i < 4; i++ {
		cdName := testPCSName + "-" + strconv.Itoa(i)
		cd := emptyComputeDomain(client.ObjectKey{Name: cdName, Namespace: testPCSNamespace})
		err := cl.Get(context.Background(), client.ObjectKeyFromObject(cd), cd)
		assert.True(t, apierrors.IsNotFound(err), "CD %s should be deleted", cdName)
	}
}

// TestSyncScaleOut tests that Sync creates new ComputeDomains when scaling up.
func TestSyncScaleOut(t *testing.T) {
	// Setup: 2 existing CDs, scale up to 4 replicas
	pcs := createPCSWithMNNVLEnabled(4)
	existingCDs := createTestCDs(testPCSName, testPCSNamespace, 2)
	cl := createTestClientWithCDs(existingCDs)
	operator := New(cl, testScheme, record.NewFakeRecorder(10))

	err := operator.Sync(context.Background(), logr.Discard(), pcs)

	require.NoError(t, err)

	// Verify all 4 CDs exist
	for i := 0; i < 4; i++ {
		cdName := testPCSName + "-" + strconv.Itoa(i)
		cd := emptyComputeDomain(client.ObjectKey{Name: cdName, Namespace: testPCSNamespace})
		err := cl.Get(context.Background(), client.ObjectKeyFromObject(cd), cd)
		assert.NoError(t, err, "CD %s should exist", cdName)
	}
}

// TestSyncIdempotent tests that Sync is idempotent - running twice with same state doesn't change anything.
func TestSyncIdempotent(t *testing.T) {
	pcs := createPCSWithMNNVLEnabled(3)
	cl := createTestClient()
	operator := New(cl, testScheme, record.NewFakeRecorder(10))

	// First sync
	err := operator.Sync(context.Background(), logr.Discard(), pcs)
	require.NoError(t, err)

	// Second sync - should be idempotent
	err = operator.Sync(context.Background(), logr.Discard(), pcs)
	require.NoError(t, err)

	// Verify exactly 3 CDs exist
	for i := 0; i < 3; i++ {
		cdName := testPCSName + "-" + strconv.Itoa(i)
		cd := emptyComputeDomain(client.ObjectKey{Name: cdName, Namespace: testPCSNamespace})
		err := cl.Get(context.Background(), client.ObjectKeyFromObject(cd), cd)
		assert.NoError(t, err, "CD %s should exist", cdName)
	}
}

// TestSyncWithGroupAnnotation tests that Sync creates group-named CDs.
func TestSyncWithGroupAnnotation(t *testing.T) {
	pcs := createPCSWithGPU(2)
	pcs.Annotations = map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}

	cl := createTestClient()
	operator := New(cl, testScheme, record.NewFakeRecorder(10))

	err := operator.Sync(context.Background(), logr.Discard(), pcs)
	require.NoError(t, err)

	expectedNames := []string{"test-pcs-0-workers", "test-pcs-1-workers"}
	for _, cdName := range expectedNames {
		cd := emptyComputeDomain(client.ObjectKey{Name: cdName, Namespace: testPCSNamespace})
		err := cl.Get(context.Background(), client.ObjectKeyFromObject(cd), cd)
		assert.NoError(t, err, "CD %s should exist", cdName)

		assert.Equal(t, "workers", cd.GetLabels()[mnnvl.LabelMNNVLGroup], "CD should have mnnvl-group label")

		rctName, found, err := unstructured.NestedString(cd.Object, "spec", "channel", "resourceClaimTemplate", "name")
		assert.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, cdName, rctName, "RCT name should match CD name")
	}
}

// ================================
// Delete Tests
// ================================

// TestDeleteRemovesAllComputeDomains tests that Delete removes all ComputeDomains when MNNVL is enabled.
func TestDeleteRemovesAllComputeDomains(t *testing.T) {
	// Create PCS with MNNVL enabled
	pcs := createPCSWithMNNVLEnabled(3)
	existingCDs := createTestCDs(testPCSName, testPCSNamespace, 3)

	// Create client with both PCS and CDs
	builder := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(pcs)
	for _, cd := range existingCDs {
		builder.WithObjects(cd)
	}
	cl := builder.Build()
	operator := New(cl, testScheme, record.NewFakeRecorder(10))

	pcsObjMeta := metav1.ObjectMeta{
		Name:      testPCSName,
		Namespace: testPCSNamespace,
		UID:       pcs.UID,
	}

	err := operator.Delete(context.Background(), logr.Discard(), pcsObjMeta)

	require.NoError(t, err)

	// Verify all CDs were deleted
	for i := 0; i < 3; i++ {
		cdName := testPCSName + "-" + strconv.Itoa(i)
		cd := emptyComputeDomain(client.ObjectKey{Name: cdName, Namespace: testPCSNamespace})
		err := cl.Get(context.Background(), client.ObjectKeyFromObject(cd), cd)
		assert.True(t, apierrors.IsNotFound(err), "CD %s should be deleted", cdName)
	}
}

// ================================
// Test Helpers
// ================================

// createPCSWithResources creates a PCS with specified container resources
func createPCSWithResources(requests, limits corev1.ResourceList) *grovecorev1alpha1.PodCliqueSet {
	return &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testPCSName,
			Namespace: testPCSNamespace,
			UID:       "pcs-uid-123",
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Replicas: 1,
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{
						Name: "clique1",
						Spec: grovecorev1alpha1.PodCliqueSpec{
							PodSpec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name:  "container1",
										Image: "alpine",
										Resources: corev1.ResourceRequirements{
											Requests: requests,
											Limits:   limits,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// createPCSWithMNNVLEnabled creates a PCS with MNNVL enabled annotation
func createPCSWithMNNVLEnabled(replicas int32) *grovecorev1alpha1.PodCliqueSet {
	pcs := createPCSWithGPU(replicas)
	pcs.Annotations = map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLEnabled}
	return pcs
}

// createPCSWithMNNVLDisabled creates a PCS with MNNVL disabled annotation (opt-out)
func createPCSWithMNNVLDisabled() *grovecorev1alpha1.PodCliqueSet {
	pcs := createPCSWithGPU(1)
	pcs.Annotations = map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLDisabled}
	return pcs
}

// createPCSWithoutGPU creates a PCS without GPU requirements
func createPCSWithoutGPU() *grovecorev1alpha1.PodCliqueSet {
	return createPCSWithResources(nil, nil)
}

// createPCSWithGPU creates a PCS with GPU requirements and specified replicas
func createPCSWithGPU(replicas int32) *grovecorev1alpha1.PodCliqueSet {
	pcs := createPCSWithResources(
		corev1.ResourceList{constants.GPUResourceName: resource.MustParse("1")},
		nil,
	)
	pcs.Spec.Replicas = replicas
	return pcs
}

// createTestClient creates a test client with testScheme that supports ComputeDomain operations.
func createTestClient() client.Client {
	return fake.NewClientBuilder().WithScheme(testScheme).Build()
}

// createTestClientWithCDs creates a test client pre-populated with ComputeDomains.
func createTestClientWithCDs(cds []*unstructured.Unstructured) client.Client {
	builder := fake.NewClientBuilder().WithScheme(testScheme)
	for _, cd := range cds {
		builder.WithObjects(cd)
	}
	return builder.Build()
}

// createClientThatFailsOnList creates a client that returns an error on List operations.
// This is used to verify that Sync skips before calling List.
func createClientThatFailsOnList() client.Client {
	return testutils.NewTestClientBuilder().
		RecordErrorForObjectsMatchingLabels(
			testutils.ClientMethodList,
			client.ObjectKey{Namespace: testPCSNamespace},
			mnnvl.ComputeDomainGVK,
			getSelectorLabels(testPCSName),
			apierrors.NewInternalError(errors.New("list should not be called")),
		).
		Build()
}

// createTestCDs creates a slice of test ComputeDomains with proper labels and finalizers.
func createTestCDs(pcsName, namespace string, count int) []*unstructured.Unstructured {
	cds := make([]*unstructured.Unstructured, count)
	for i := 0; i < count; i++ {
		cdName := pcsName + "-" + strconv.Itoa(i)
		cd := createTestCD(cdName, namespace, pcsName, i)
		cds[i] = cd
	}
	return cds
}

// createTestCD creates a single test ComputeDomain with proper labels, finalizer, and owner reference.
func createTestCD(name, namespace, pcsName string, replicaIndex int) *unstructured.Unstructured {
	cd := &unstructured.Unstructured{}
	cd.SetGroupVersionKind(mnnvl.ComputeDomainGVK)
	cd.SetName(name)
	cd.SetNamespace(namespace)

	// Set labels matching what the operator creates
	cd.SetLabels(map[string]string{
		apicommon.LabelManagedByKey:             apicommon.LabelManagedByValue,
		apicommon.LabelPartOfKey:                pcsName,
		apicommon.LabelAppNameKey:               name,
		apicommon.LabelComponentKey:             labelComponentNameComputeDomain,
		apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(replicaIndex),
	})

	// Set finalizer
	cd.SetFinalizers([]string{mnnvl.FinalizerComputeDomain})

	// Set owner reference - required for FilterMapOwnedResourceNames to find the CD
	cd.SetOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion: "grove.io/v1alpha1",
			Kind:       "PodCliqueSet",
			Name:       pcsName,
			UID:        "pcs-uid-123",
			Controller: boolPtr(true),
		},
	})

	// Set spec with RCT reference (using correct nested structure)
	_ = unstructured.SetNestedField(cd.Object, name, "spec", "channel", "resourceClaimTemplate", "name")
	_ = unstructured.SetNestedField(cd.Object, int64(0), "spec", "numNodes")

	return cd
}

// boolPtr returns a pointer to a bool value.
func boolPtr(b bool) *bool {
	return &b
}
