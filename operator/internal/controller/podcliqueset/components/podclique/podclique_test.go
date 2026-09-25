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

package podclique

import (
	"context"
	"fmt"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/mnnvl"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	testPCSName      = "coyote"
	testPCSNamespace = "cobalt-ns"
)

func TestGetExistingResourceNames(t *testing.T) {
	testCases := []struct {
		description                 string
		pcsReplicas                 int32
		podCliqueTemplateNames      []string
		podCliqueNamesNotOwnedByPCS []string
		expectedPodCliqueNames      []string
		listErr                     *apierrors.StatusError
		expectedErr                 *groveerr.GroveError
	}{
		{
			description:            "PodCliqueSet has zero replicas and one PodClique",
			pcsReplicas:            0,
			podCliqueTemplateNames: []string{"howl"},
			expectedPodCliqueNames: []string{},
		},
		{
			description:            "PodCliqueSet has one replica and two PodCliques",
			pcsReplicas:            1,
			podCliqueTemplateNames: []string{"howl", "grin"},
			expectedPodCliqueNames: []string{"coyote-0-howl", "coyote-0-grin"},
		},
		{
			description:            "PodCliqueSet has two replicas and two PodCliques",
			pcsReplicas:            3,
			podCliqueTemplateNames: []string{"howl", "grin"},
			expectedPodCliqueNames: []string{"coyote-0-howl", "coyote-0-grin", "coyote-1-howl", "coyote-1-grin", "coyote-2-howl", "coyote-2-grin"},
		},
		{
			description:                 "PodCliqueSet has two replicas and two PodCliques with one not owned by the PodCliqueSet",
			pcsReplicas:                 2,
			podCliqueTemplateNames:      []string{"howl"},
			podCliqueNamesNotOwnedByPCS: []string{"bandit"},
			expectedPodCliqueNames:      []string{"coyote-0-howl", "coyote-1-howl"},
		},
		{
			description:            "should return error when list fails",
			pcsReplicas:            2,
			podCliqueTemplateNames: []string{"howl"},
			listErr:                testutils.TestAPIInternalErr,
			expectedErr: &groveerr.GroveError{
				Code:      errCodeListPodCliques,
				Cause:     testutils.TestAPIInternalErr,
				Operation: component.OperationGetExistingResourceNames,
			},
		},
	}

	t.Parallel()
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()
			// Create a PodCliqueSet with the specified number of replicas and PodCliques
			pcsBuilder := testutils.NewPodCliqueSetBuilder(testPCSName, testPCSNamespace, uuid.NewUUID()).
				WithReplicas(tc.pcsReplicas).
				WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder))
			for _, pclqTemplateName := range tc.podCliqueTemplateNames {
				pcsBuilder.WithPodCliqueParameters(pclqTemplateName, 1, nil)
			}
			pcs := pcsBuilder.Build()
			// Create existing objects
			existingObjects := createExistingPodCliquesFromPCS(pcs, tc.podCliqueNamesNotOwnedByPCS)
			// Create a fake client with PodCliques. The list-error record is keyed by the list object's
			// GVK (PodCliqueList), which is what the typed PodCliqueList List call resolves to.
			cl := testutils.CreateFakeClientForObjectsMatchingLabels(nil, tc.listErr, pcs.Namespace, grovecorev1alpha1.SchemeGroupVersion.WithKind("PodCliqueList"), getPodCliqueSelectorLabels(pcs.ObjectMeta), existingObjects...)
			operator := New(cl, groveclientscheme.Scheme, record.NewFakeRecorder(10))
			actualPCLQNames, err := operator.GetExistingResourceNames(context.Background(), logr.Discard(), pcs.ObjectMeta)
			if tc.expectedErr == nil {
				assert.NoError(t, err)
				assert.ElementsMatch(t, tc.expectedPodCliqueNames, actualPCLQNames)
			} else {
				testutils.AssertGroveError(t, tc.expectedErr, err)
			}
		})
	}
}

func TestDelete(t *testing.T) {
	testCases := []struct {
		description           string
		numExistingPodCliques int
		deleteError           *apierrors.StatusError
		expectedError         *groveerr.GroveError
	}{
		{
			description:           "no-op when there are no existing PodCliques",
			numExistingPodCliques: 0,
			expectedError:         nil,
		},
		{
			description:           "successfully delete all existing PodCliques",
			numExistingPodCliques: 2,
			expectedError:         nil,
		},
		{
			description:           "error when deleting existing PodCliques",
			numExistingPodCliques: 2,
			deleteError:           testutils.TestAPIInternalErr,
			expectedError: &groveerr.GroveError{
				Code:      errDeletePodClique,
				Cause:     testutils.TestAPIInternalErr,
				Operation: component.OperationDelete,
			},
		},
	}

	t.Parallel()
	pcsObjMeta := metav1.ObjectMeta{
		Name:      testPCSName,
		Namespace: testPCSNamespace,
		UID:       uuid.NewUUID(),
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()
			existingPodCliques := createDefaultPodCliques(pcsObjMeta, "howl", tc.numExistingPodCliques)
			// Create a fake client with PodCliques
			cl := testutils.CreateFakeClientForObjectsMatchingLabels(tc.deleteError, nil, testPCSNamespace, grovecorev1alpha1.SchemeGroupVersion.WithKind("PodClique"), getPodCliqueSelectorLabels(pcsObjMeta), existingPodCliques...)
			operator := New(cl, groveclientscheme.Scheme, record.NewFakeRecorder(10))
			err := operator.Delete(context.Background(), logr.Discard(), pcsObjMeta)
			if tc.expectedError != nil {
				testutils.AssertGroveError(t, tc.expectedError, err)
			} else {
				assert.NoError(t, err)
				podCliquesPostDelete := getExistingPodCliques(t, cl, pcsObjMeta)
				assert.Empty(t, podCliquesPostDelete)
			}
		})
	}
}

func getExistingPodCliques(t *testing.T, cl client.Client, pcsObjMeta metav1.ObjectMeta) []grovecorev1alpha1.PodClique {
	podCliqueList := &grovecorev1alpha1.PodCliqueList{}
	assert.NoError(t, cl.List(context.Background(), podCliqueList, client.InNamespace(pcsObjMeta.Namespace), client.MatchingLabels(getPodCliqueSelectorLabels(pcsObjMeta))))
	return podCliqueList.Items
}

func createDefaultPodCliques(pcsObjMeta metav1.ObjectMeta, pclqNamePrefix string, numPodCliques int) []client.Object {
	podCliqueNames := make([]client.Object, 0, numPodCliques)
	for i := range numPodCliques {
		pclq := testutils.NewPodCliqueBuilder(pcsObjMeta.Name, pcsObjMeta.GetUID(), fmt.Sprintf("%s-%d", pclqNamePrefix, i), pcsObjMeta.Namespace, 0).
			WithLabels(getPodCliqueSelectorLabels(pcsObjMeta)).
			Build()
		podCliqueNames = append(podCliqueNames, pclq)
	}
	return podCliqueNames
}

func createExistingPodCliquesFromPCS(pcs *grovecorev1alpha1.PodCliqueSet, podCliqueNamesNotOwnedByPCS []string) []client.Object {
	existingPodCliques := make([]client.Object, 0, len(pcs.Spec.Template.Cliques)*int(pcs.Spec.Replicas)+len(podCliqueNamesNotOwnedByPCS))
	for replicaIndex := range pcs.Spec.Replicas {
		for _, pclqTemplate := range pcs.Spec.Template.Cliques {
			pclq := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, pclqTemplate.Name, pcs.Namespace, replicaIndex).
				WithLabels(getPodCliqueSelectorLabels(pcs.ObjectMeta)).
				WithStartsAfter(pclqTemplate.Spec.StartsAfter).
				Build()
			existingPodCliques = append(existingPodCliques, pclq)
		}
	}
	// Add additional PodCliques not owned by the PodCliqueSet
	nonExistingPCSName := "ebony"
	for _, podCliqueName := range podCliqueNamesNotOwnedByPCS {
		pclq := testutils.NewPodCliqueBuilder(nonExistingPCSName, uuid.NewUUID(), podCliqueName, pcs.Namespace, 0).
			WithOwnerReference("PodCliqueSet", nonExistingPCSName, uuid.NewUUID()).Build()
		existingPodCliques = append(existingPodCliques, pclq)
	}
	return existingPodCliques
}

func TestBuildResource_MNNVLInjection(t *testing.T) {
	tests := []struct {
		description                         string
		pcsAnnotations                      map[string]string
		cliqueAnnotations                   map[string]string
		containers                          []corev1.Container
		initContainers                      []corev1.Container
		expectedContainersWithClaims        []string
		expectedContainersWithoutClaims     []string
		expectedInitContainersWithClaims    []string
		expectedInitContainersWithoutClaims []string
		expectPodLevelClaim                 bool
		expectedRCTName                     string
	}{
		{
			description: "MNNVL enabled with GPU container injects claims",
			pcsAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: "default",
			},
			containers: []corev1.Container{
				{
					Name: "gpu-worker",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							constants.GPUResourceName: resource.MustParse("8"),
						},
					},
				},
			},
			expectedContainersWithClaims:    []string{"gpu-worker"},
			expectedContainersWithoutClaims: []string{},
			expectPodLevelClaim:             true,
			expectedRCTName:                 "coyote-0-default",
		},
		{
			description:    "MNNVL not enabled does not inject claims",
			pcsAnnotations: nil,
			containers: []corev1.Container{
				{
					Name: "gpu-worker",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							constants.GPUResourceName: resource.MustParse("8"),
						},
					},
				},
			},
			expectedContainersWithClaims:    []string{},
			expectedContainersWithoutClaims: []string{"gpu-worker"},
			expectPodLevelClaim:             false,
		},
		{
			description: "MNNVL enabled but no GPU containers does not inject claims",
			pcsAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: "default",
			},
			containers: []corev1.Container{
				{
					Name: "cpu-only",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					},
				},
			},
			expectedContainersWithClaims:    []string{},
			expectedContainersWithoutClaims: []string{"cpu-only"},
			expectPodLevelClaim:             false,
		},
		{
			description: "MNNVL enabled with mixed GPU and non-GPU containers",
			pcsAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: "default",
			},
			containers: []corev1.Container{
				{
					Name: "gpu-worker",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							constants.GPUResourceName: resource.MustParse("8"),
						},
					},
				},
				{
					Name: "sidecar",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					},
				},
			},
			expectedContainersWithClaims:    []string{"gpu-worker"},
			expectedContainersWithoutClaims: []string{"sidecar"},
			expectPodLevelClaim:             true,
		},
		{
			description: "MNNVL enabled with GPU in init container",
			pcsAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: "default",
			},
			initContainers: []corev1.Container{
				{
					Name: "init-gpu",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							constants.GPUResourceName: resource.MustParse("1"),
						},
					},
				},
			},
			containers: []corev1.Container{
				{Name: "main"},
			},
			expectedContainersWithClaims:        []string{},
			expectedContainersWithoutClaims:     []string{"main"},
			expectedInitContainersWithClaims:    []string{"init-gpu"},
			expectedInitContainersWithoutClaims: []string{},
			expectPodLevelClaim:                 true,
		},
		{
			description: "MNNVL disabled explicitly does not inject claims",
			pcsAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: mnnvl.AnnotationMNNVLGroupOptOut,
			},
			containers: []corev1.Container{
				{
					Name: "gpu-worker",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							constants.GPUResourceName: resource.MustParse("8"),
						},
					},
				},
			},
			expectedContainersWithClaims:    []string{},
			expectedContainersWithoutClaims: []string{"gpu-worker"},
			expectPodLevelClaim:             false,
		},
		{
			description: "mnnvl-group on PCS — RCT name includes group",
			pcsAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: "workers",
			},
			containers: []corev1.Container{
				{
					Name: "gpu-worker",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							constants.GPUResourceName: resource.MustParse("8"),
						},
					},
				},
			},
			expectedContainersWithClaims:    []string{"gpu-worker"},
			expectedContainersWithoutClaims: []string{},
			expectPodLevelClaim:             true,
			expectedRCTName:                 "coyote-0-workers",
		},
		{
			description: "mnnvl-group on clique overrides PCS auto-mnnvl",
			pcsAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: "default",
			},
			cliqueAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: "encoders",
			},
			containers: []corev1.Container{
				{
					Name: "gpu-worker",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							constants.GPUResourceName: resource.MustParse("8"),
						},
					},
				},
			},
			expectedContainersWithClaims:    []string{"gpu-worker"},
			expectedContainersWithoutClaims: []string{},
			expectPodLevelClaim:             true,
			expectedRCTName:                 "coyote-0-encoders",
		},
		{
			description: "mnnvl-group on clique only — no PCS annotation",
			cliqueAnnotations: map[string]string{
				mnnvl.AnnotationMNNVLGroup: "training",
			},
			containers: []corev1.Container{
				{
					Name: "gpu-worker",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							constants.GPUResourceName: resource.MustParse("8"),
						},
					},
				},
			},
			expectedContainersWithClaims:    []string{"gpu-worker"},
			expectedContainersWithoutClaims: []string{},
			expectPodLevelClaim:             true,
			expectedRCTName:                 "coyote-0-training",
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			pcsReplica := 0
			pclqTemplateName := "worker"

			// Build PCS with the test case's containers
			pcsBuilder := testutils.NewPodCliqueSetBuilder(testPCSName, testPCSNamespace, uuid.NewUUID()).
				WithReplicas(1).
				WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder)).
				WithAnnotations(tc.pcsAnnotations)

			// Create PodCliqueTemplateSpec with containers and optional annotations
			pclqTemplateSpec := testutils.NewPodCliqueTemplateSpecBuilder(pclqTemplateName).
				WithAnnotations(tc.cliqueAnnotations).
				Build()
			pclqTemplateSpec.Spec.PodSpec.Containers = tc.containers
			pclqTemplateSpec.Spec.PodSpec.InitContainers = tc.initContainers
			pcsBuilder.WithPodCliqueTemplateSpec(pclqTemplateSpec)

			pcs := pcsBuilder.Build()

			// Create empty PodClique with matching name suffix
			pclqName := fmt.Sprintf("%s-%d-%s", testPCSName, pcsReplica, pclqTemplateName)
			pclq := &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:      pclqName,
					Namespace: testPCSNamespace,
				},
			}

			// Create operator and call buildResource
			operator := &_resource{
				client:        nil, // not needed for buildResource
				scheme:        groveclientscheme.Scheme,
				eventRecorder: record.NewFakeRecorder(10),
			}

			// A standalone PodClique belongs to the anchor entry, so buildResource resolves its PodGang
			// name from the anchor entry's epoch.
			pgm := testutils.NewPodGangMapBuilder(testPCSName, testPCSNamespace, uuid.NewUUID(), pcsReplica).WithEntries(
				testutils.NewPodGangEntryBuilder("hash", "1000").
					WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).Build(),
			).Build()
			err := operator.buildResource(logr.Discard(), pcs, pcsReplica, false, pgm, pclq)
			require.NoError(t, err)

			// Verify pod-level claims
			if tc.expectPodLevelClaim {
				require.Len(t, pclq.Spec.PodSpec.ResourceClaims, 1, "expected pod-level MNNVL claim")
				assert.Equal(t, mnnvl.MNNVLClaimName, pclq.Spec.PodSpec.ResourceClaims[0].Name)
				if tc.expectedRCTName != "" {
					require.NotNil(t, pclq.Spec.PodSpec.ResourceClaims[0].ResourceClaimTemplateName)
					assert.Equal(t, tc.expectedRCTName, *pclq.Spec.PodSpec.ResourceClaims[0].ResourceClaimTemplateName)
				}
			} else {
				assert.Empty(t, pclq.Spec.PodSpec.ResourceClaims, "expected no pod-level claims")
			}

			// Verify container claims
			withClaims, withoutClaims := triageContainersByMNNVLClaim(pclq.Spec.PodSpec.Containers)
			assert.ElementsMatch(t, tc.expectedContainersWithClaims, withClaims,
				"containers with MNNVL claims should match expected")
			assert.ElementsMatch(t, tc.expectedContainersWithoutClaims, withoutClaims,
				"containers without MNNVL claims should match expected")

			// Verify init container claims
			initWithClaims, initWithoutClaims := triageContainersByMNNVLClaim(pclq.Spec.PodSpec.InitContainers)
			assert.ElementsMatch(t, tc.expectedInitContainersWithClaims, initWithClaims,
				"init containers with MNNVL claims should match expected")
			assert.ElementsMatch(t, tc.expectedInitContainersWithoutClaims, initWithoutClaims,
				"init containers without MNNVL claims should match expected")
		})
	}
}

func TestBuildResource_StripsTopologyAnnotation(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testPCSNamespace, uuid.NewUUID()).
		WithReplicas(1).
		WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder)).
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder("worker").
				WithAnnotations(map[string]string{
					apiconstants.AnnotationTopologyName: "my-topology",
					"example.com/keep":                  "yes",
				}).
				Build(),
		).
		Build()

	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-0-worker", testPCSName),
			Namespace: testPCSNamespace,
		},
	}

	operator := &_resource{scheme: groveclientscheme.Scheme}
	// A standalone PodClique belongs to the anchor entry, so buildResource resolves its PodGang name
	// from the anchor entry's epoch.
	pgm := testutils.NewPodGangMapBuilder(testPCSName, testPCSNamespace, uuid.NewUUID(), 0).WithEntries(
		testutils.NewPodGangEntryBuilder("hash", "1000").
			WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).Build(),
	).Build()
	err := operator.buildResource(logr.Discard(), pcs, 0, false, pgm, pclq)
	require.NoError(t, err)
	require.NotNil(t, pclq.Annotations)
	assert.Equal(t, "yes", pclq.Annotations["example.com/keep"])
	_, hasTopologyAnnotation := pclq.Annotations[apiconstants.AnnotationTopologyName]
	assert.False(t, hasTopologyAnnotation)
}

// TestBuildResource_PreservesRevisionForReplicaNotUnderCoherentUpdate verifies that during a coherent
// update the new pod template is applied only to the replica under update, while every other replica keeps
// its running PodSpec and hash label so a pod recreated on it does not adopt the new revision.
func TestBuildResource_PreservesRevisionForReplicaNotUnderCoherentUpdate(t *testing.T) {
	const cliqueName = "worker"
	testCases := []struct {
		description     string
		coherentUpdate  bool
		pcsReplica      int
		expectPreserved bool
	}{
		{"a coherent update leaves a replica that is not under update on its running revision", true, 1, true},
		{"a coherent update rolls the replica that is under update to the new revision", true, 0, false},
		{"no update in progress applies the template to every replica", false, 1, false},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			pcsBuilder := testutils.NewPodCliqueSetBuilder(testPCSName, testPCSNamespace, uuid.NewUUID()).
				WithReplicas(2).
				WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder)).
				WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy})
			template := testutils.NewPodCliqueTemplateSpecBuilder(cliqueName).Build()
			template.Spec.PodSpec.Containers = []corev1.Container{{Name: "c", Image: "new"}}
			pcsBuilder.WithPodCliqueTemplateSpec(template)
			if tc.coherentUpdate {
				pcsBuilder.WithUpdateProgress(&grovecorev1alpha1.PodCliqueSetUpdateProgress{
					UpdateStartedAt:   metav1.Now(),
					CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}},
				})
			}
			pcs := pcsBuilder.Build()

			// An existing PodClique on the running (old) revision.
			pclq := &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-%d-%s", testPCSName, tc.pcsReplica, cliqueName),
					Namespace: testPCSNamespace,
					Labels:    map[string]string{apicommon.LabelPodTemplateHash: "old-hash"},
				},
				Spec: grovecorev1alpha1.PodCliqueSpec{
					PodSpec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "old"}}},
				},
			}

			operator := &_resource{scheme: groveclientscheme.Scheme}
			pgm := testutils.NewPodGangMapBuilder(testPCSName, testPCSNamespace, uuid.NewUUID(), tc.pcsReplica).WithEntries(
				testutils.NewPodGangEntryBuilder("hash", "1000").
					WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).Build(),
			).Build()

			err := operator.buildResource(logr.Discard(), pcs, tc.pcsReplica, true, pgm, pclq)
			require.NoError(t, err)

			if tc.expectPreserved {
				assert.Equal(t, "old", pclq.Spec.PodSpec.Containers[0].Image, "PodSpec must keep the running revision")
				assert.Equal(t, "old-hash", pclq.Labels[apicommon.LabelPodTemplateHash], "hash label must keep the running revision")
			} else {
				assert.Equal(t, "new", pclq.Spec.PodSpec.Containers[0].Image, "PodSpec must adopt the new revision")
				assert.NotEqual(t, "old-hash", pclq.Labels[apicommon.LabelPodTemplateHash], "hash label must adopt the new revision")
			}
		})
	}
}

// TestBuildResource_ExplicitStartsAfterStaysSinglePrefixed reconciles a preserved replica twice under the
// Explicit startup type and asserts StartsAfter stays the single-prefix FQN. It guards against re-prefixing
// the live StartsAfter (which already holds FQNs) instead of reading the template's clique names.
func TestBuildResource_ExplicitStartsAfterStaysSinglePrefixed(t *testing.T) {
	const (
		leaderClique = "leader"
		workerClique = "worker"
		pcsReplica   = 1
	)
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testPCSNamespace, uuid.NewUUID()).
		WithReplicas(2).
		WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeExplicit)).
		WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy}).
		WithUpdateProgress(&grovecorev1alpha1.PodCliqueSetUpdateProgress{
			UpdateStartedAt:   metav1.Now(),
			CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}},
		}).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder(leaderClique).Build()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder(workerClique).WithStartsAfter([]string{leaderClique}).Build()).
		Build()

	// The worker PodClique on the preserved replica already holds the resolved FQN from a previous reconcile.
	wantStartsAfter := []string{fmt.Sprintf("%s-%d-%s", testPCSName, pcsReplica, leaderClique)}
	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d-%s", testPCSName, pcsReplica, workerClique),
			Namespace: testPCSNamespace,
			Labels:    map[string]string{apicommon.LabelPodTemplateHash: "old-hash"},
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{StartsAfter: wantStartsAfter},
	}

	operator := &_resource{scheme: groveclientscheme.Scheme}
	pgm := testutils.NewPodGangMapBuilder(testPCSName, testPCSNamespace, uuid.NewUUID(), pcsReplica).WithEntries(
		testutils.NewPodGangEntryBuilder("hash", "1000").WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).Build(),
	).Build()

	// Reconcile twice. StartsAfter must remain the single-prefix FQN both times.
	for i := range 2 {
		err := operator.buildResource(logr.Discard(), pcs, pcsReplica, true, pgm, pclq)
		require.NoError(t, err, "reconcile %d", i)
		assert.Equal(t, wantStartsAfter, pclq.Spec.StartsAfter, "reconcile %d", i)
	}
}

// triageContainersByMNNVLClaim separates containers into those with MNNVL claim and those without.
func triageContainersByMNNVLClaim(containers []corev1.Container) (withClaim, withoutClaim []string) {
	for _, c := range containers {
		hasClaim := false
		for _, claim := range c.Resources.Claims {
			if claim.Name == mnnvl.MNNVLClaimName {
				hasClaim = true
				break
			}
		}
		if hasClaim {
			withClaim = append(withClaim, c.Name)
		} else {
			withoutClaim = append(withoutClaim, c.Name)
		}
	}
	return withClaim, withoutClaim
}
