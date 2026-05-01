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

package validation

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	groveconfigv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	schedmanager "github.com/ai-dynamo/grove/operator/internal/scheduler/manager"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestResourceNamingValidation(t *testing.T) {
	testCases := []struct {
		description   string
		pcsName       string
		cliqueNames   []string
		scalingGroups []grovecorev1alpha1.PodCliqueScalingGroupConfig
		errorMatchers []testutils.ErrorMatcher
	}{
		{
			description: "Valid resource names",
			pcsName:     "inference",
			cliqueNames: []string{"prefill", "decode"},
			scalingGroups: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				createScalingGroupConfig("workers", []string{"prefill", "decode"}),
			},
		},
		{
			description: "PodClique template name exceeds character limit",
			pcsName:     "verylongpodcliquesetnamethatisverylong",
			cliqueNames: []string{"verylongpodcliquenamethatexceedslimit"},
			scalingGroups: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				createScalingGroupConfig("workers-1", []string{"prefill-1", "decode-1"}),
				createScalingGroupConfig("verylongpodcliquenamethatexceedslimit-2", []string{"prefill", "decode"}),
				createScalingGroupConfig("verylongpodcliquenamethatexceedslimit-3", []string{"prefill", "decode"}),
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.cliques[0].name"},
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroups[1].name"},
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroups[2].name"},
			},
		},
		{
			description: "Empty PodClique template name",
			pcsName:     "inference",
			cliqueNames: []string{""},
			errorMatchers: []testutils.ErrorMatcher{
				// TODO: @unmarshall @renormalize only one should be required here, fix later
				{ErrorType: field.ErrorTypeRequired, Field: "spec.template.cliques[0].name"},
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.cliques[0].name"},
			},
		},
		{
			description: "PodClique template name with invalid characters",
			pcsName:     "inference",
			cliqueNames: []string{"prefill_worker"},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.cliques[0].name"},
			},
		},
		{
			description:   "Scaling group with long names",
			pcsName:       "verylongpodcliquesetname",
			cliqueNames:   []string{"verylongpodcliquename"},
			scalingGroups: []grovecorev1alpha1.PodCliqueScalingGroupConfig{createScalingGroupConfig("verylongscalinggroup", []string{"verylongpodcliquename"})},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroups[0].name"},
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroups[0].cliqueNames[0].name"},
				{ErrorType: field.ErrorTypeInvalid, Field: "metadata.name"},
			},
		},
		{
			description:   "Scaling group referencing non-existent PodClique",
			pcsName:       "inference",
			cliqueNames:   []string{"prefill"},
			scalingGroups: []grovecorev1alpha1.PodCliqueScalingGroupConfig{createScalingGroupConfig("workers", []string{"nonexistent"})},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroups[0].cliqueNames"},
			},
		},
		{
			description:   "Maximum valid character usage",
			pcsName:       "pcs",
			cliqueNames:   []string{"cliquename20charssss"},
			scalingGroups: []grovecorev1alpha1.PodCliqueScalingGroupConfig{createScalingGroupConfig("sg", []string{"cliquename20charssss"})},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			pcsBuilder := testutils.NewPodCliqueSetBuilder(tc.pcsName, "default", uuid.NewUUID()).
				WithReplicas(1).
				WithTerminationDelay(4 * time.Hour).
				WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder))

			// Add PodClique templates
			for _, cliqueName := range tc.cliqueNames {
				clique := testutils.NewPodCliqueTemplateSpecBuilder(cliqueName).
					WithReplicas(1).
					WithRoleName(fmt.Sprintf("dummy-%s-role", cliqueName)).
					WithMinAvailable(1).
					Build()
				pcsBuilder = pcsBuilder.WithPodCliqueTemplateSpec(clique)
			}

			// Add scaling groups
			for _, config := range tc.scalingGroups {
				pcsBuilder = pcsBuilder.WithPodCliqueScalingGroupConfig(config)
			}

			pcs := pcsBuilder.Build()

			validator := newPCSValidator(pcs, admissionv1.Create, defaultTASConfig(), groveconfigv1alpha1.SchedulerConfiguration{Profiles: []groveconfigv1alpha1.SchedulerProfile{{Name: groveconfigv1alpha1.SchedulerNameKube}}, DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube)}, nil)
			warnings, errs := validator.validate()

			if tc.errorMatchers != nil {
				testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
			} else {
				assert.NoError(t, errs.ToAggregate(), "Expected no validation error for test case: %s", tc.description)
			}

			assert.Empty(t, warnings, "No warnings expected for these test cases")
		})
	}
}

func TestValidateSchedulerNames(t *testing.T) {
	specPath := field.NewPath("cliques").Child("spec").Child("podSpec").Child("schedulerName")
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)

	tests := []struct {
		name                 string
		schedulerConfig      groveconfigv1alpha1.SchedulerConfiguration
		schedulerNames       []string
		expectErrors         int
		expectInvalidSame    bool
		expectInvalidEnabled bool
	}{
		{
			name: "all same default-scheduler (kube default)",
			schedulerConfig: groveconfigv1alpha1.SchedulerConfiguration{
				Profiles: []groveconfigv1alpha1.SchedulerProfile{
					{Name: groveconfigv1alpha1.SchedulerNameKube},
					{Name: groveconfigv1alpha1.SchedulerNameKai},
				},
				DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube),
			},
			schedulerNames: []string{"default-scheduler", "default-scheduler"},
			expectErrors:   0,
		},
		{
			name: "all empty with default default-scheduler",
			schedulerConfig: groveconfigv1alpha1.SchedulerConfiguration{
				Profiles: []groveconfigv1alpha1.SchedulerProfile{
					{Name: groveconfigv1alpha1.SchedulerNameKube},
					{Name: groveconfigv1alpha1.SchedulerNameKai},
				},
				DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube),
			},
			schedulerNames: []string{"", ""},
			expectErrors:   0,
		},
		{
			name: "all empty with default kai-scheduler (kai default)",
			schedulerConfig: groveconfigv1alpha1.SchedulerConfiguration{
				Profiles: []groveconfigv1alpha1.SchedulerProfile{
					{Name: groveconfigv1alpha1.SchedulerNameKube},
					{Name: groveconfigv1alpha1.SchedulerNameKai},
				},
				DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKai),
			},
			schedulerNames: []string{"", ""},
			expectErrors:   0,
		},
		{
			name: "mixed empty and default-scheduler",
			schedulerConfig: groveconfigv1alpha1.SchedulerConfiguration{
				Profiles: []groveconfigv1alpha1.SchedulerProfile{
					{Name: groveconfigv1alpha1.SchedulerNameKube},
					{Name: groveconfigv1alpha1.SchedulerNameKai},
				},
				DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube),
			},
			schedulerNames: []string{"", "default-scheduler"},
			expectErrors:   0,
		},
		{
			name: "mixed default-scheduler and kai-scheduler",
			schedulerConfig: groveconfigv1alpha1.SchedulerConfiguration{
				Profiles: []groveconfigv1alpha1.SchedulerProfile{
					{Name: groveconfigv1alpha1.SchedulerNameKube},
					{Name: groveconfigv1alpha1.SchedulerNameKai},
				},
				DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube),
			},
			schedulerNames:       []string{"default-scheduler", "kai-scheduler"},
			expectErrors:         1,
			expectInvalidSame:    true,
			expectInvalidEnabled: false,
		},
		{
			name: "single kai-scheduler when enabled (kube+kai)",
			schedulerConfig: groveconfigv1alpha1.SchedulerConfiguration{
				Profiles: []groveconfigv1alpha1.SchedulerProfile{
					{Name: groveconfigv1alpha1.SchedulerNameKube},
					{Name: groveconfigv1alpha1.SchedulerNameKai},
				},
				DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube),
			},
			schedulerNames: []string{"kai-scheduler"},
			expectErrors:   0,
		},
		{
			name: "single default-scheduler when enabled (kube only)",
			schedulerConfig: groveconfigv1alpha1.SchedulerConfiguration{
				Profiles:           []groveconfigv1alpha1.SchedulerProfile{{Name: groveconfigv1alpha1.SchedulerNameKube}},
				DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube),
			},
			schedulerNames:       []string{"kai-scheduler"},
			expectErrors:         1,
			expectInvalidSame:    false,
			expectInvalidEnabled: true,
		},
		{
			name: "unknown scheduler not enabled",
			schedulerConfig: groveconfigv1alpha1.SchedulerConfiguration{
				Profiles: []groveconfigv1alpha1.SchedulerProfile{
					{Name: groveconfigv1alpha1.SchedulerNameKube},
					{Name: groveconfigv1alpha1.SchedulerNameKai},
				},
				DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube),
			},
			schedulerNames:       []string{"volcano"},
			expectErrors:         1,
			expectInvalidSame:    false,
			expectInvalidEnabled: true,
		},
		{
			name: "no cliques (empty list)",
			schedulerConfig: groveconfigv1alpha1.SchedulerConfiguration{
				Profiles:           []groveconfigv1alpha1.SchedulerProfile{{Name: groveconfigv1alpha1.SchedulerNameKube}},
				DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube),
			},
			schedulerNames: []string{},
			expectErrors:   0,
		},
		{
			name: "mixed empty and kai when default is default-scheduler",
			schedulerConfig: groveconfigv1alpha1.SchedulerConfiguration{
				Profiles: []groveconfigv1alpha1.SchedulerProfile{
					{Name: groveconfigv1alpha1.SchedulerNameKube},
					{Name: groveconfigv1alpha1.SchedulerNameKai},
				},
				DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube),
			},
			schedulerNames:       []string{"", "kai-scheduler"},
			expectErrors:         1,
			expectInvalidSame:    true,
			expectInvalidEnabled: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := schedmanager.Initialize(cl, cl.Scheme(), recorder, tt.schedulerConfig)
			require.NoError(t, err)

			pcsBuilder := testutils.NewPodCliqueSetBuilder("test", "default", uuid.NewUUID()).
				WithReplicas(1).
				WithTerminationDelay(4 * time.Hour).
				WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder))
			for i := 0; i < len(tt.schedulerNames); i++ {
				clique := createDummyPodCliqueTemplate(fmt.Sprintf("c%d", i))
				clique.Spec.PodSpec.SchedulerName = tt.schedulerNames[i]
				pcsBuilder = pcsBuilder.WithPodCliqueTemplateSpec(clique)
			}
			pcs := pcsBuilder.Build()
			validator := newPCSValidator(pcs, admissionv1.Create, defaultTASConfig(), tt.schedulerConfig, nil)
			fldPath := field.NewPath("cliques")
			errs := validator.validateSchedulerNames(tt.schedulerNames, fldPath)

			assert.Len(t, errs, tt.expectErrors, "validation errors: %v", errs)
			if tt.expectErrors > 0 {
				msgs := lo.Map(errs, func(e *field.Error, _ int) string { return e.ErrorBody() })
				if tt.expectInvalidSame {
					assert.Contains(t, strings.Join(msgs, " "), "have to be the same")
				}
				if tt.expectInvalidEnabled {
					assert.Contains(t, strings.Join(msgs, " "), "not enabled")
				}
			}
			for _, e := range errs {
				assert.Equal(t, specPath.String(), e.Field, "error field path")
			}
		})
	}
}

func TestPodCliqueScalingGroupConfigValidation(t *testing.T) {
	testCases := []struct {
		description     string
		pcsName         string
		scalingGroups   []grovecorev1alpha1.PodCliqueScalingGroupConfig
		cliqueTemplates []string
		errorMatchers   []testutils.ErrorMatcher
	}{
		{
			description: "Valid scaling group with Replicas and MinAvailable",
			pcsName:     "inference",
			scalingGroups: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:         "workers",
					CliqueNames:  []string{"prefill"},
					Replicas:     ptr.To(int32(4)),
					MinAvailable: ptr.To(int32(2)),
				},
			},
			cliqueTemplates: []string{"prefill"},
		},
		{
			description: "Invalid Replicas (negative value)",
			pcsName:     "inference",
			scalingGroups: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:         "workers",
					CliqueNames:  []string{"prefill"},
					Replicas:     ptr.To(int32(-1)),
					MinAvailable: ptr.To(int32(1)),
				},
			},
			cliqueTemplates: []string{"prefill"},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroups[0].replicas"},
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroups[0].minAvailable"},
			},
		},
		{
			description: "Invalid MinAvailable (zero value)",
			pcsName:     "inference",
			scalingGroups: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:         "workers",
					CliqueNames:  []string{"prefill"},
					Replicas:     ptr.To(int32(4)),
					MinAvailable: ptr.To(int32(0)),
				},
			},
			cliqueTemplates: []string{"prefill"},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroups[0].minAvailable"},
			},
		},
		{
			description: "Invalid MinAvailable > Replicas",
			pcsName:     "inference",
			scalingGroups: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:         "workers",
					CliqueNames:  []string{"prefill"},
					Replicas:     ptr.To(int32(2)),
					MinAvailable: ptr.To(int32(4)),
				},
			},
			cliqueTemplates: []string{"prefill"},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroups[0].minAvailable"},
			},
		},
		{
			description: "Invalid ScaleConfig.MinReplicas < MinAvailable",
			pcsName:     "inference",
			scalingGroups: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:         "workers",
					CliqueNames:  []string{"prefill"},
					Replicas:     ptr.To(int32(4)),
					MinAvailable: ptr.To(int32(3)),
					ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{
						MinReplicas: ptr.To(int32(2)),
						MaxReplicas: 10,
					},
				},
			},
			cliqueTemplates: []string{"prefill"},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroups[0].scaleConfig.minReplicas"},
			},
		},
		{
			description: "Valid with partial configuration",
			pcsName:     "inference",
			scalingGroups: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:        "workers",
					CliqueNames: []string{"prefill"},
					Replicas:    ptr.To(int32(4)),
				},
			},
			cliqueTemplates: []string{"prefill"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			pcs := createTestPodCliqueSet(tc.pcsName)

			// Add PodClique templates
			for _, cliqueName := range tc.cliqueTemplates {
				pcs.Spec.Template.Cliques = append(pcs.Spec.Template.Cliques, createDummyPodCliqueTemplate(cliqueName))
			}

			// Add scaling groups
			pcs.Spec.Template.PodCliqueScalingGroupConfigs = tc.scalingGroups

			validator := newPCSValidator(pcs, admissionv1.Create, defaultTASConfig(), groveconfigv1alpha1.SchedulerConfiguration{Profiles: []groveconfigv1alpha1.SchedulerProfile{{Name: groveconfigv1alpha1.SchedulerNameKube}}, DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube)}, nil)
			warnings, errs := validator.validate()

			if tc.errorMatchers != nil {
				testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
			} else {
				assert.NoError(t, errs.ToAggregate(), "Expected no validation error for test case: %s", tc.description)
			}
			assert.Empty(t, warnings, "No warnings expected for these test cases")
		})
	}
}

func TestPodCliqueUpdateValidation(t *testing.T) {
	testCases := []struct {
		name           string
		startupType    *grovecorev1alpha1.CliqueStartupType
		oldCliques     []*grovecorev1alpha1.PodCliqueTemplateSpec
		newCliques     []*grovecorev1alpha1.PodCliqueTemplateSpec
		expectError    bool
		expectedErrMsg string
	}{
		{
			name:        "Valid: same cliques in different order with AnyOrder",
			startupType: ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder),
			oldCliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				createDummyPodCliqueTemplate("prefill"),
				createDummyPodCliqueTemplate("decode"),
			},
			newCliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				createDummyPodCliqueTemplate("decode"),
				createDummyPodCliqueTemplate("prefill"),
			},
			expectError: false,
		},
		{
			name:        "Invalid: adding new clique",
			startupType: ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder),
			oldCliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				createDummyPodCliqueTemplate("prefill"),
			},
			newCliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				createDummyPodCliqueTemplate("prefill"),
				createDummyPodCliqueTemplate("decode"),
			},
			expectError:    true,
			expectedErrMsg: "not allowed to change clique composition",
		},
		{
			name:        "Invalid: removing clique",
			startupType: ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder),
			oldCliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				createDummyPodCliqueTemplate("prefill"),
				createDummyPodCliqueTemplate("decode"),
			},
			newCliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				createDummyPodCliqueTemplate("prefill"),
			},
			expectError:    true,
			expectedErrMsg: "not allowed to change clique composition",
		},
		{
			name:        "Invalid: InOrder doesn't allow order change",
			startupType: ptr.To(grovecorev1alpha1.CliqueStartupTypeInOrder),
			oldCliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				createDummyPodCliqueTemplate("prefill"),
				createDummyPodCliqueTemplate("decode"),
			},
			newCliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				createDummyPodCliqueTemplate("decode"),
				createDummyPodCliqueTemplate("prefill"),
			},
			expectError:    true,
			expectedErrMsg: "clique order cannot be changed when StartupType is InOrder or Explicit",
		},
		{
			name:        "Invalid: Explicit doesn't allow order change",
			startupType: ptr.To(grovecorev1alpha1.CliqueStartupTypeExplicit),
			oldCliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				createDummyPodCliqueTemplate("prefill"),
				createDummyPodCliqueTemplate("decode"),
			},
			newCliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				createDummyPodCliqueTemplate("decode"),
				createDummyPodCliqueTemplate("prefill"),
			},
			expectError:    true,
			expectedErrMsg: "clique order cannot be changed when StartupType is InOrder or Explicit",
		},
		{
			name:        "Valid: InOrder allows same order",
			startupType: ptr.To(grovecorev1alpha1.CliqueStartupTypeInOrder),
			oldCliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				createDummyPodCliqueTemplate("prefill"),
				createDummyPodCliqueTemplate("decode"),
			},
			newCliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				createDummyPodCliqueTemplate("prefill"),
				createDummyPodCliqueTemplate("decode"),
			},
			expectError: false,
		},
		{
			name:        "Edge case: empty arrays",
			startupType: ptr.To(grovecorev1alpha1.CliqueStartupTypeInOrder),
			oldCliques:  []*grovecorev1alpha1.PodCliqueTemplateSpec{},
			newCliques:  []*grovecorev1alpha1.PodCliqueTemplateSpec{},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create old and new PCS objects
			oldPCS := createTestPodCliqueSet("test")
			oldPCS.Spec.Template.StartupType = tc.startupType
			oldPCS.Spec.Template.Cliques = tc.oldCliques

			newPCS := createTestPodCliqueSet("test")
			newPCS.Spec.Template.StartupType = tc.startupType
			newPCS.Spec.Template.Cliques = tc.newCliques

			// Create validator and validate update
			validator := newPCSValidator(newPCS, admissionv1.Update, defaultTASConfig(), groveconfigv1alpha1.SchedulerConfiguration{Profiles: []groveconfigv1alpha1.SchedulerProfile{{Name: groveconfigv1alpha1.SchedulerNameKube}}, DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube)}, nil)
			fldPath := field.NewPath("spec").Child("template").Child("cliques")
			validationErrors := validator.validatePodCliqueUpdate(oldPCS.Spec.Template.Cliques, fldPath)

			if tc.expectError {
				assert.NotEmpty(t, validationErrors, "Expected validation errors for test case: %s", tc.name)
				var errorMessages []string
				for _, err := range validationErrors {
					errorMessages = append(errorMessages, err.Error())
				}
				errorString := fmt.Sprintf("%v", errorMessages)
				assert.Contains(t, errorString, tc.expectedErrMsg, "Error message should contain expected text")
			} else {
				assert.Empty(t, validationErrors, "Expected no validation errors for test case: %s", tc.name)
			}
		})
	}
}

func TestImmutableFieldsValidation(t *testing.T) {
	testCases := []struct {
		name           string
		setupOldPCS    func() *grovecorev1alpha1.PodCliqueSet
		setupNewPCS    func() *grovecorev1alpha1.PodCliqueSet
		expectError    bool
		expectedErrMsg string
	}{
		{
			name: "Valid: PriorityClassName can be updated",
			setupOldPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("test")
				pcs.Spec.Template.PriorityClassName = "old-priority"
				return pcs
			},
			setupNewPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("test")
				pcs.Spec.Template.PriorityClassName = "new-priority"
				return pcs
			},
			expectError: false,
		},
		{
			name: "Invalid: RoleName is immutable",
			setupOldPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("test")
				pcs.Spec.Template.Cliques[0].Spec.RoleName = "old-role"
				return pcs
			},
			setupNewPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("test")
				pcs.Spec.Template.Cliques[0].Spec.RoleName = "new-role"
				return pcs
			},
			expectError:    true,
			expectedErrMsg: "field is immutable",
		},
		{
			name: "Invalid: MinAvailable is immutable",
			setupOldPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("test")
				pcs.Spec.Template.Cliques[0].Spec.MinAvailable = ptr.To(int32(1))
				return pcs
			},
			setupNewPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("test")
				pcs.Spec.Template.Cliques[0].Spec.MinAvailable = ptr.To(int32(2))
				return pcs
			},
			expectError:    true,
			expectedErrMsg: "field is immutable",
		},
		{
			name: "Invalid: StartsAfter is immutable",
			setupOldPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("test")
				pcs.Spec.Template.StartupType = ptr.To(grovecorev1alpha1.CliqueStartupTypeExplicit)
				pcs.Spec.Template.Cliques = append(pcs.Spec.Template.Cliques, createDummyPodCliqueTemplate("clique2"))
				pcs.Spec.Template.Cliques[0].Spec.StartsAfter = []string{}
				pcs.Spec.Template.Cliques[1].Spec.StartsAfter = []string{"test"}
				return pcs
			},
			setupNewPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("test")
				pcs.Spec.Template.StartupType = ptr.To(grovecorev1alpha1.CliqueStartupTypeExplicit)
				pcs.Spec.Template.Cliques = append(pcs.Spec.Template.Cliques, createDummyPodCliqueTemplate("clique2"))
				pcs.Spec.Template.Cliques[0].Spec.StartsAfter = []string{}
				pcs.Spec.Template.Cliques[1].Spec.StartsAfter = []string{"test", "another"}
				return pcs
			},
			expectError:    true,
			expectedErrMsg: "field is immutable",
		},
		{
			name: "Edge case: Multiple immutable field violations",
			setupOldPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("test")
				pcs.Spec.Template.Cliques[0].Spec.RoleName = "old-role"
				pcs.Spec.Template.Cliques[0].Spec.MinAvailable = ptr.To(int32(1))
				pcs.Spec.Template.Cliques[0].Spec.StartsAfter = []string{"dep1"}
				return pcs
			},
			setupNewPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("test")
				pcs.Spec.Template.Cliques[0].Spec.RoleName = "new-role"
				pcs.Spec.Template.Cliques[0].Spec.MinAvailable = ptr.To(int32(2))
				pcs.Spec.Template.Cliques[0].Spec.StartsAfter = []string{"dep1", "dep2"}
				return pcs
			},
			expectError:    true,
			expectedErrMsg: "field is immutable",
		},
		{
			name: "Invalid: schedulerName is immutable",
			setupOldPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("test")
				pcs.Spec.Template.Cliques[0].Spec.PodSpec.SchedulerName = ""
				return pcs
			},
			setupNewPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("test")
				pcs.Spec.Template.Cliques[0].Spec.PodSpec.SchedulerName = "default-scheduler"
				return pcs
			},
			expectError:    true,
			expectedErrMsg: "field is immutable",
		},
		{
			name: "Invalid: PCS resourceClaimTemplates is immutable",
			setupOldPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("test")
				pcs.Spec.Template.ResourceClaimTemplates = []grovecorev1alpha1.ResourceClaimTemplateConfig{
					{Name: "tpl-a", TemplateSpec: resourcev1.ResourceClaimTemplateSpec{}},
				}
				return pcs
			},
			setupNewPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("test")
				pcs.Spec.Template.ResourceClaimTemplates = []grovecorev1alpha1.ResourceClaimTemplateConfig{
					{Name: "tpl-b", TemplateSpec: resourcev1.ResourceClaimTemplateSpec{}},
				}
				return pcs
			},
			expectError:    true,
			expectedErrMsg: "field is immutable",
		},
		{
			name: "Invalid: PCS resourceSharing is immutable",
			setupOldPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("test")
				pcs.Spec.Template.ResourceSharing = []grovecorev1alpha1.PCSResourceSharingSpec{
					{ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{Name: "share-a", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas}},
				}
				return pcs
			},
			setupNewPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("test")
				pcs.Spec.Template.ResourceSharing = []grovecorev1alpha1.PCSResourceSharingSpec{
					{ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{Name: "share-a", Scope: grovecorev1alpha1.ResourceSharingScopePerReplica}},
				}
				return pcs
			},
			expectError:    true,
			expectedErrMsg: "field is immutable",
		},
		{
			name: "Invalid: PCLQ resourceSharing is immutable",
			setupOldPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("test")
				pcs.Spec.Template.Cliques[0].ResourceSharing = []grovecorev1alpha1.ResourceSharingSpec{
					{Name: "share-a", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
				}
				return pcs
			},
			setupNewPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("test")
				pcs.Spec.Template.Cliques[0].ResourceSharing = []grovecorev1alpha1.ResourceSharingSpec{
					{Name: "share-a", Scope: grovecorev1alpha1.ResourceSharingScopePerReplica},
				}
				return pcs
			},
			expectError:    true,
			expectedErrMsg: "field is immutable",
		},
		{
			name: "Valid: PCS resourceClaimTemplates unchanged",
			setupOldPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("test")
				pcs.Spec.Template.ResourceClaimTemplates = []grovecorev1alpha1.ResourceClaimTemplateConfig{
					{Name: "tpl-a", TemplateSpec: resourcev1.ResourceClaimTemplateSpec{}},
				}
				return pcs
			},
			setupNewPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("test")
				pcs.Spec.Template.ResourceClaimTemplates = []grovecorev1alpha1.ResourceClaimTemplateConfig{
					{Name: "tpl-a", TemplateSpec: resourcev1.ResourceClaimTemplateSpec{}},
				}
				return pcs
			},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			oldPCS := tc.setupOldPCS()
			newPCS := tc.setupNewPCS()

			validator := newPCSValidator(newPCS, admissionv1.Update, defaultTASConfig(), groveconfigv1alpha1.SchedulerConfiguration{Profiles: []groveconfigv1alpha1.SchedulerProfile{{Name: groveconfigv1alpha1.SchedulerNameKube}}, DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube)}, nil)
			err := validator.validateUpdate(oldPCS)

			if tc.expectError {
				assert.Error(t, err, "Expected validation error for test case: %s", tc.name)
				assert.Contains(t, err.Error(), tc.expectedErrMsg, "Error message should contain expected text")
			} else {
				assert.NoError(t, err, "Expected no validation error for test case: %s", tc.name)
			}
		})
	}
}

func TestPodCliqueScalingGroupConfigsUpdateValidation(t *testing.T) {
	tests := []struct {
		name           string
		oldConfigs     []grovecorev1alpha1.PodCliqueScalingGroupConfig
		newConfigs     []grovecorev1alpha1.PodCliqueScalingGroupConfig
		expectedErrors bool
		expectedErrMsg string
	}{
		{
			name: "same configs - should pass",
			oldConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					CliqueNames:  []string{"clique1", "clique2"},
					MinAvailable: ptr.To(int32(1)),
				},
			},
			newConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					CliqueNames:  []string{"clique1", "clique2"},
					MinAvailable: ptr.To(int32(1)),
				},
			},
			expectedErrors: false,
		},
		{
			name: "different clique names - should fail",
			oldConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					CliqueNames:  []string{"clique1", "clique2"},
					MinAvailable: ptr.To(int32(1)),
				},
			},
			newConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					CliqueNames:  []string{"clique1", "clique3"},
					MinAvailable: ptr.To(int32(1)),
				},
			},
			expectedErrors: true,
			expectedErrMsg: "field is immutable",
		},
		{
			name: "different min available - should fail",
			oldConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					CliqueNames:  []string{"clique1", "clique2"},
					MinAvailable: ptr.To(int32(1)),
				},
			},
			newConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					CliqueNames:  []string{"clique1", "clique2"},
					MinAvailable: ptr.To(int32(2)),
				},
			},
			expectedErrors: true,
			expectedErrMsg: "field is immutable",
		},
		{
			name: "adding new config - should fail",
			oldConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					CliqueNames:  []string{"clique1"},
					MinAvailable: ptr.To(int32(1)),
				},
			},
			newConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					CliqueNames:  []string{"clique1"},
					MinAvailable: ptr.To(int32(1)),
				},
				{
					CliqueNames:  []string{"clique2"},
					MinAvailable: ptr.To(int32(1)),
				},
			},
			expectedErrors: true,
			expectedErrMsg: "not allowed to add or remove",
		},
		{
			name: "removing config - should fail",
			oldConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					CliqueNames:  []string{"clique1"},
					MinAvailable: ptr.To(int32(1)),
				},
				{
					CliqueNames:  []string{"clique2"},
					MinAvailable: ptr.To(int32(1)),
				},
			},
			newConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					CliqueNames:  []string{"clique1"},
					MinAvailable: ptr.To(int32(1)),
				},
			},
			expectedErrors: true,
			expectedErrMsg: "not allowed to add or remove",
		},
		{
			name:           "nil to empty slice - should pass",
			oldConfigs:     nil,
			newConfigs:     []grovecorev1alpha1.PodCliqueScalingGroupConfig{},
			expectedErrors: false,
		},
		{
			name:           "empty slice to nil - should pass",
			oldConfigs:     []grovecorev1alpha1.PodCliqueScalingGroupConfig{},
			newConfigs:     nil,
			expectedErrors: false,
		},
		{
			name: "nil min available in both - should pass",
			oldConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					CliqueNames:  []string{"clique1"},
					MinAvailable: nil,
				},
			},
			newConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					CliqueNames:  []string{"clique1"},
					MinAvailable: nil,
				},
			},
			expectedErrors: false,
		},
		{
			name: "nil to non-nil min available - should fail",
			oldConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					CliqueNames:  []string{"clique1"},
					MinAvailable: nil,
				},
			},
			newConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					CliqueNames:  []string{"clique1"},
					MinAvailable: ptr.To(int32(1)),
				},
			},
			expectedErrors: true,
			expectedErrMsg: "field is immutable",
		},
		{
			name: "same resourceSharing - should pass",
			oldConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:         "sg1",
					CliqueNames:  []string{"clique1"},
					MinAvailable: ptr.To(int32(1)),
					ResourceSharing: []grovecorev1alpha1.PCSGResourceSharingSpec{
						{ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{Name: "share-a", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas}},
					},
				},
			},
			newConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:         "sg1",
					CliqueNames:  []string{"clique1"},
					MinAvailable: ptr.To(int32(1)),
					ResourceSharing: []grovecorev1alpha1.PCSGResourceSharingSpec{
						{ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{Name: "share-a", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas}},
					},
				},
			},
			expectedErrors: false,
		},
		{
			name: "different resourceSharing - should fail",
			oldConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:         "sg1",
					CliqueNames:  []string{"clique1"},
					MinAvailable: ptr.To(int32(1)),
					ResourceSharing: []grovecorev1alpha1.PCSGResourceSharingSpec{
						{ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{Name: "share-a", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas}},
					},
				},
			},
			newConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:         "sg1",
					CliqueNames:  []string{"clique1"},
					MinAvailable: ptr.To(int32(1)),
					ResourceSharing: []grovecorev1alpha1.PCSGResourceSharingSpec{
						{ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{Name: "share-a", Scope: grovecorev1alpha1.ResourceSharingScopePerReplica}},
					},
				},
			},
			expectedErrors: true,
			expectedErrMsg: "field is immutable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Create old and new PCS objects
			oldPCS := createTestPodCliqueSet("test")
			oldPCS.Spec.Template.PodCliqueScalingGroupConfigs = tc.oldConfigs

			newPCS := createTestPodCliqueSet("test")
			newPCS.Spec.Template.PodCliqueScalingGroupConfigs = tc.newConfigs

			// Create validator and validate update
			validator := newPCSValidator(newPCS, admissionv1.Update, defaultTASConfig(), groveconfigv1alpha1.SchedulerConfiguration{Profiles: []groveconfigv1alpha1.SchedulerProfile{{Name: groveconfigv1alpha1.SchedulerNameKube}}, DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube)}, nil)
			fldPath := field.NewPath("spec", "template", "podCliqueScalingGroupConfigs")
			validationErrors := validator.validatePodCliqueScalingGroupConfigsUpdate(tc.oldConfigs, fldPath)

			if tc.expectedErrors {
				assert.NotEmpty(t, validationErrors, "Expected validation errors for test case: %s", tc.name)
				var errorMessages []string
				for _, err := range validationErrors {
					errorMessages = append(errorMessages, err.Error())
				}
				errorString := fmt.Sprintf("%v", errorMessages)
				assert.Contains(t, errorString, tc.expectedErrMsg, "Error message should contain expected text")
			} else {
				assert.Empty(t, validationErrors, "Expected no validation errors for test case: %s", tc.name)
			}
		})
	}
}

// TestValidateCliqueDependencies tests validation of clique dependencies for cycles and unknown cliques.
func TestValidateCliqueDependencies(t *testing.T) {
	fldPath := field.NewPath("spec", "template", "cliques")

	tests := []struct {
		// name identifies this test case
		name string
		// cliques is the list of clique templates to validate
		cliques []*grovecorev1alpha1.PodCliqueTemplateSpec
		// expectError indicates whether validation should fail
		expectError bool
		// errorContains is a substring expected in the error message
		errorContains string
	}{
		{
			name: "valid dependencies with no cycles",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "clique1",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						StartsAfter: []string{},
					},
				},
				{
					Name: "clique2",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						StartsAfter: []string{"clique1"},
					},
				},
			},
			expectError: false,
		},
		{
			name: "circular dependency between two cliques",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "clique1",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						StartsAfter: []string{"clique2"},
					},
				},
				{
					Name: "clique2",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						StartsAfter: []string{"clique1"},
					},
				},
			},
			expectError:   true,
			errorContains: "circular dependencies",
		},
		{
			name: "dependency on unknown clique",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "clique1",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						StartsAfter: []string{"unknown-clique"},
					},
				},
			},
			expectError:   true,
			errorContains: "unknown clique names found",
		},
		{
			name: "three-way circular dependency",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "clique1",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						StartsAfter: []string{"clique3"},
					},
				},
				{
					Name: "clique2",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						StartsAfter: []string{"clique1"},
					},
				},
				{
					Name: "clique3",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						StartsAfter: []string{"clique2"},
					},
				},
			},
			expectError:   true,
			errorContains: "circular dependencies",
		},
		{
			name: "no dependencies passes validation",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "clique1",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						StartsAfter: []string{},
					},
				},
			},
			expectError: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			errs := validateCliqueDependencies(test.cliques, fldPath)
			if test.expectError {
				assert.NotEmpty(t, errs)
				if test.errorContains != "" {
					errorString := ""
					for _, err := range errs {
						errorString += err.Error()
					}
					assert.Contains(t, errorString, test.errorContains)
				}
			} else {
				assert.Empty(t, errs)
			}
		})
	}
}

// TestValidateScaleConfig tests validation of autoscaling configuration.
func TestValidateScaleConfig(t *testing.T) {
	fldPath := field.NewPath("spec", "autoScalingConfig")

	tests := []struct {
		// name identifies this test case
		name string
		// scaleConfig is the autoscaling configuration to validate
		scaleConfig *grovecorev1alpha1.AutoScalingConfig
		// minAvailable is the minimum available pods
		minAvailable int32
		// expectError indicates whether validation should fail
		expectError bool
		// errorContains is a substring expected in the error message
		errorContains string
	}{
		{
			name: "valid scale config",
			scaleConfig: &grovecorev1alpha1.AutoScalingConfig{
				MinReplicas: ptr.To(int32(2)),
				MaxReplicas: 5,
			},
			minAvailable: 1,
			expectError:  false,
		},
		{
			name: "minReplicas less than minAvailable returns error",
			scaleConfig: &grovecorev1alpha1.AutoScalingConfig{
				MinReplicas: ptr.To(int32(1)),
				MaxReplicas: 5,
			},
			minAvailable:  2,
			expectError:   true,
			errorContains: "must be greater than or equal to podCliqueSpec.minAvailable",
		},
		{
			name: "maxReplicas less than minReplicas returns error",
			scaleConfig: &grovecorev1alpha1.AutoScalingConfig{
				MinReplicas: ptr.To(int32(5)),
				MaxReplicas: 3,
			},
			minAvailable:  1,
			expectError:   true,
			errorContains: "must be greater than or equal to podCliqueSpec.minReplicas",
		},
		{
			name: "minReplicas equal to maxReplicas passes validation",
			scaleConfig: &grovecorev1alpha1.AutoScalingConfig{
				MinReplicas: ptr.To(int32(5)),
				MaxReplicas: 5,
			},
			minAvailable: 1,
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateScaleConfig(tt.scaleConfig, tt.minAvailable, fldPath)
			if tt.expectError {
				assert.NotEmpty(t, errs)
				if tt.errorContains != "" {
					errorString := ""
					for _, err := range errs {
						errorString += err.Error()
					}
					assert.Contains(t, errorString, tt.errorContains)
				}
			} else {
				assert.Empty(t, errs)
			}
		})
	}
}

func TestEnvVarValidation(t *testing.T) {
	testCases := []struct {
		description    string
		containers     []corev1.Container
		initContainers []corev1.Container
		errorMatchers  []testutils.ErrorMatcher
	}{
		{
			description: "Valid env var names",
			containers: []corev1.Container{
				{
					Name:  "main",
					Image: "test:latest",
					Env: []corev1.EnvVar{
						{Name: "MY_VAR"},
						{Name: "_PRIVATE"},
						{Name: "Var123"},
						{Name: "MY-VAR"},
						{Name: "my.var"},
					},
				},
			},
		},
		{
			description: "Env var name starting with digit",
			containers: []corev1.Container{
				{
					Name:  "main",
					Image: "test:latest",
					Env:   []corev1.EnvVar{{Name: "1VAR"}},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.cliques[0].spec.podSpec.spec.containers[0].env[0].name"},
			},
		},
		{
			description: "Duplicate env var names in same container",
			containers: []corev1.Container{
				{
					Name:  "main",
					Image: "test:latest",
					Env: []corev1.EnvVar{
						{Name: "MY_VAR"},
						{Name: "MY_VAR"},
					},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeDuplicate, Field: "spec.template.cliques[0].spec.podSpec.spec.containers[0].env"},
			},
		},
		{
			description: "Duplicate env var names in initContainers",
			initContainers: []corev1.Container{
				{
					Name:  "init",
					Image: "test:latest",
					Env: []corev1.EnvVar{
						{Name: "INIT_VAR"},
						{Name: "INIT_VAR"},
					},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeDuplicate, Field: "spec.template.cliques[0].spec.podSpec.spec.initContainers[0].env"},
			},
		},
		{
			description: "Same env var name in different containers is valid",
			containers: []corev1.Container{
				{
					Name:  "first",
					Image: "test:latest",
					Env:   []corev1.EnvVar{{Name: "SHARED_VAR"}},
				},
				{
					Name:  "second",
					Image: "test:latest",
					Env:   []corev1.EnvVar{{Name: "SHARED_VAR"}},
				},
			},
		},
		{
			description: "Empty env list",
			containers: []corev1.Container{
				{
					Name:  "main",
					Image: "test:latest",
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			clique := testutils.NewPodCliqueTemplateSpecBuilder("worker").
				WithReplicas(1).
				WithRoleName("dummy-worker-role").
				WithMinAvailable(1)

			for _, c := range tc.containers {
				clique = clique.WithContainer(c)
			}
			for _, c := range tc.initContainers {
				clique = clique.WithInitContainer(c)
			}

			pcs := testutils.NewPodCliqueSetBuilder("inference", "default", uuid.NewUUID()).
				WithReplicas(1).
				WithTerminationDelay(4 * time.Hour).
				WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder)).
				WithPodCliqueTemplateSpec(clique.Build()).
				Build()

			validator := newPCSValidator(pcs, admissionv1.Create, defaultTASConfig(), groveconfigv1alpha1.SchedulerConfiguration{Profiles: []groveconfigv1alpha1.SchedulerProfile{{Name: groveconfigv1alpha1.SchedulerNameKube}}, DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube)}, nil)
			_, errs := validator.validate()

			if tc.errorMatchers != nil {
				testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
			} else {
				assert.NoError(t, errs.ToAggregate(), "Expected no validation error for test case: %s", tc.description)
			}
		})
	}
}

// ---------------------------- Resource Sharing Validation Tests ----------------------------

func TestValidateResourceClaimTemplates(t *testing.T) {
	tests := []struct {
		name          string
		templates     []grovecorev1alpha1.ResourceClaimTemplateConfig
		errorMatchers []testutils.ErrorMatcher
	}{
		{
			name: "valid unique names",
			templates: []grovecorev1alpha1.ResourceClaimTemplateConfig{
				{Name: "gpu-mps", TemplateSpec: resourcev1.ResourceClaimTemplateSpec{Spec: resourcev1.ResourceClaimSpec{Devices: resourcev1.DeviceClaim{Requests: []resourcev1.DeviceRequest{{Name: "gpu"}}}}}},
				{Name: "shared-mem", TemplateSpec: resourcev1.ResourceClaimTemplateSpec{Spec: resourcev1.ResourceClaimSpec{Devices: resourcev1.DeviceClaim{Requests: []resourcev1.DeviceRequest{{Name: "mem"}}}}}},
			},
		},
		{
			name: "empty name",
			templates: []grovecorev1alpha1.ResourceClaimTemplateConfig{
				{Name: ""},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeRequired, Field: "spec.template.resourceClaimTemplates[0].name"},
				{ErrorType: field.ErrorTypeRequired, Field: "spec.template.resourceClaimTemplates[0].templateSpec.spec.devices.requests"},
			},
		},
		{
			name: "empty template spec",
			templates: []grovecorev1alpha1.ResourceClaimTemplateConfig{
				{Name: "gpu-mps"},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeRequired, Field: "spec.template.resourceClaimTemplates[0].templateSpec.spec.devices.requests"},
			},
		},
		{
			name: "duplicate names",
			templates: []grovecorev1alpha1.ResourceClaimTemplateConfig{
				{Name: "gpu-mps"},
				{Name: "gpu-mps"},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeRequired, Field: "spec.template.resourceClaimTemplates[0].templateSpec.spec.devices.requests"},
				{ErrorType: field.ErrorTypeRequired, Field: "spec.template.resourceClaimTemplates[1].templateSpec.spec.devices.requests"},
				{ErrorType: field.ErrorTypeDuplicate, Field: "spec.template.resourceClaimTemplates.name"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := createTestPodCliqueSet("my-pcs")
			pcs.Spec.Template.ResourceClaimTemplates = tc.templates

			validator := newPCSValidator(pcs, admissionv1.Create, defaultTASConfig(), groveconfigv1alpha1.SchedulerConfiguration{Profiles: []groveconfigv1alpha1.SchedulerProfile{{Name: groveconfigv1alpha1.SchedulerNameKube}}, DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube)}, nil)
			fldPath := field.NewPath("spec", "template", "resourceClaimTemplates")
			errs := validator.validateResourceClaimTemplates(fldPath)

			if tc.errorMatchers != nil {
				testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
			} else {
				assert.Empty(t, errs)
			}
		})
	}
}

func TestValidateResourceSharingSpecs(t *testing.T) {
	tests := []struct {
		name          string
		templates     []grovecorev1alpha1.ResourceClaimTemplateConfig
		refs          []grovecorev1alpha1.ResourceSharingSpec
		errorMatchers []testutils.ErrorMatcher
	}{
		{
			name:      "valid refs",
			templates: []grovecorev1alpha1.ResourceClaimTemplateConfig{{Name: "gpu-mps"}},
			refs: []grovecorev1alpha1.ResourceSharingSpec{
				{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
			},
		},
		{
			name: "empty ref name",
			refs: []grovecorev1alpha1.ResourceSharingSpec{
				{Name: "", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeRequired, Field: "spec.template.resourceSharing[0].name"},
			},
		},
		{
			name:      "duplicate refs",
			templates: []grovecorev1alpha1.ResourceClaimTemplateConfig{{Name: "gpu-mps"}},
			refs: []grovecorev1alpha1.ResourceSharingSpec{
				{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
				{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopePerReplica},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeDuplicate, Field: "spec.template.resourceSharing[1].name"},
			},
		},
		{
			name:      "namespace set for internal template",
			templates: []grovecorev1alpha1.ResourceClaimTemplateConfig{{Name: "gpu-mps"}},
			refs: []grovecorev1alpha1.ResourceSharingSpec{
				{Name: "gpu-mps", Namespace: "other-ns", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.resourceSharing[0].namespace"},
			},
		},
		{
			name: "external ref (no internal match) with namespace is valid",
			refs: []grovecorev1alpha1.ResourceSharingSpec{
				{Name: "external-tpl", Namespace: "shared-ns", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
			},
		},
		{
			name: "invalid scope",
			refs: []grovecorev1alpha1.ResourceSharingSpec{
				{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScope("BadScope")},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeNotSupported, Field: "spec.template.resourceSharing[0].scope"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := createTestPodCliqueSet("my-pcs")
			pcs.Spec.Template.ResourceClaimTemplates = tc.templates

			validator := newPCSValidator(pcs, admissionv1.Create, defaultTASConfig(), groveconfigv1alpha1.SchedulerConfiguration{Profiles: []groveconfigv1alpha1.SchedulerProfile{{Name: groveconfigv1alpha1.SchedulerNameKube}}, DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube)}, nil)
			fldPath := field.NewPath("spec", "template", "resourceSharing")
			errs := validator.validateResourceSharingSpecs(tc.refs, fldPath)

			if tc.errorMatchers != nil {
				testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
			} else {
				assert.Empty(t, errs)
			}
		})
	}
}

func TestValidatePCSResourceSharing(t *testing.T) {
	tests := []struct {
		name          string
		cliqueNames   []string
		groupConfigs  []grovecorev1alpha1.PodCliqueScalingGroupConfig
		refs          []grovecorev1alpha1.PCSResourceSharingSpec
		errorMatchers []testutils.ErrorMatcher
	}{
		{
			name:        "valid filter with known clique name",
			cliqueNames: []string{"worker"},
			refs: []grovecorev1alpha1.PCSResourceSharingSpec{
				{
					ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
					Filter:              &grovecorev1alpha1.PCSResourceSharingFilter{ChildCliqueNames: []string{"worker"}},
				},
			},
		},
		{
			name:        "valid filter with known group name",
			cliqueNames: []string{"worker"},
			groupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sga", CliqueNames: []string{"worker"}},
			},
			refs: []grovecorev1alpha1.PCSResourceSharingSpec{
				{
					ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
					Filter:              &grovecorev1alpha1.PCSResourceSharingFilter{ChildScalingGroupNames: []string{"sga"}},
				},
			},
		},
		{
			name:        "invalid clique name in filter",
			cliqueNames: []string{"worker"},
			refs: []grovecorev1alpha1.PCSResourceSharingSpec{
				{
					ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
					Filter:              &grovecorev1alpha1.PCSResourceSharingFilter{ChildCliqueNames: []string{"nonexistent"}},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeNotFound, Field: "spec.template.resourceSharing[0].filter.childCliqueNames[0]"},
			},
		},
		{
			name:        "invalid group name in filter",
			cliqueNames: []string{"worker"},
			refs: []grovecorev1alpha1.PCSResourceSharingSpec{
				{
					ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
					Filter:              &grovecorev1alpha1.PCSResourceSharingFilter{ChildScalingGroupNames: []string{"nonexistent"}},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeNotFound, Field: "spec.template.resourceSharing[0].filter.childScalingGroupNames[0]"},
			},
		},
		{
			name:        "empty filter (no names specified)",
			cliqueNames: []string{"worker"},
			refs: []grovecorev1alpha1.PCSResourceSharingSpec{
				{
					ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
					Filter:              &grovecorev1alpha1.PCSResourceSharingFilter{},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeRequired, Field: "spec.template.resourceSharing[0].filter"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := createTestPodCliqueSet("my-pcs")
			pcs.Spec.Template.Cliques = nil
			for _, name := range tc.cliqueNames {
				pcs.Spec.Template.Cliques = append(pcs.Spec.Template.Cliques, createDummyPodCliqueTemplate(name))
			}
			pcs.Spec.Template.PodCliqueScalingGroupConfigs = tc.groupConfigs
			pcs.Spec.Template.ResourceSharing = tc.refs

			validator := newPCSValidator(pcs, admissionv1.Create, defaultTASConfig(), groveconfigv1alpha1.SchedulerConfiguration{Profiles: []groveconfigv1alpha1.SchedulerProfile{{Name: groveconfigv1alpha1.SchedulerNameKube}}, DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube)}, nil)
			fldPath := field.NewPath("spec", "template", "resourceSharing")
			errs := validator.validatePCSResourceSharing(tc.refs, fldPath)

			if tc.errorMatchers != nil {
				testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
			} else {
				assert.Empty(t, errs)
			}
		})
	}
}

func TestValidatePCSGResourceSharing(t *testing.T) {
	tests := []struct {
		name          string
		cfg           grovecorev1alpha1.PodCliqueScalingGroupConfig
		templates     []grovecorev1alpha1.ResourceClaimTemplateConfig
		errorMatchers []testutils.ErrorMatcher
	}{
		{
			name: "valid PCSG sharing with clique filter",
			cfg: grovecorev1alpha1.PodCliqueScalingGroupConfig{
				Name:        "sga",
				CliqueNames: []string{"worker"},
				ResourceSharing: []grovecorev1alpha1.PCSGResourceSharingSpec{
					{
						ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
						Filter:              &grovecorev1alpha1.PCSGResourceSharingFilter{ChildCliqueNames: []string{"worker"}},
					},
				},
			},
			templates: []grovecorev1alpha1.ResourceClaimTemplateConfig{{Name: "gpu-mps"}},
		},
		{
			name: "invalid clique name in PCSG filter",
			cfg: grovecorev1alpha1.PodCliqueScalingGroupConfig{
				Name:        "sga",
				CliqueNames: []string{"worker"},
				ResourceSharing: []grovecorev1alpha1.PCSGResourceSharingSpec{
					{
						ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
						Filter:              &grovecorev1alpha1.PCSGResourceSharingFilter{ChildCliqueNames: []string{"unknown-clique"}},
					},
				},
			},
			templates: []grovecorev1alpha1.ResourceClaimTemplateConfig{{Name: "gpu-mps"}},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeNotFound, Field: "spec.template.podCliqueScalingGroups[0].resourceSharing[0].filter.childCliqueNames[0]"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := createTestPodCliqueSet("my-pcs")
			pcs.Spec.Template.ResourceClaimTemplates = tc.templates
			// Add cliques referenced by the PCSG
			for _, cn := range tc.cfg.CliqueNames {
				pcs.Spec.Template.Cliques = append(pcs.Spec.Template.Cliques, createDummyPodCliqueTemplate(cn))
			}

			validator := newPCSValidator(pcs, admissionv1.Create, defaultTASConfig(), groveconfigv1alpha1.SchedulerConfiguration{Profiles: []groveconfigv1alpha1.SchedulerProfile{{Name: groveconfigv1alpha1.SchedulerNameKube}}, DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube)}, nil)
			fldPath := field.NewPath("spec", "template", "podCliqueScalingGroups").Index(0).Child("resourceSharing")
			errs := validator.validatePCSGResourceSharing(tc.cfg, fldPath)

			if tc.errorMatchers != nil {
				testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
			} else {
				assert.Empty(t, errs)
			}
		})
	}
}

func TestValidateTopologyConstraintsPCSTopologyName(t *testing.T) {
	schedulerConfig := groveconfigv1alpha1.SchedulerConfiguration{
		Profiles:           []groveconfigv1alpha1.SchedulerProfile{{Name: groveconfigv1alpha1.SchedulerNameKube}},
		DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube),
	}
	tasConfig := groveconfigv1alpha1.TopologyAwareSchedulingConfiguration{Enabled: true}

	tests := []struct {
		name          string
		operation     admissionv1.Operation
		setupOldPCS   func() *grovecorev1alpha1.PodCliqueSet
		setupNewPCS   func() *grovecorev1alpha1.PodCliqueSet
		clusterObjs   []client.Object
		errorMatchers []testutils.ErrorMatcher
	}{
		{
			name:      "create allows child-only explicit topology constraint",
			operation: admissionv1.Create,
			setupNewPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("pcs-topology-create")
				pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
					TopologyName: "topo-a",
					PackDomain:   grovecorev1alpha1.TopologyDomainHost,
				}
				return pcs
			},
			clusterObjs: []client.Object{createTestClusterTopology()},
		},
		{
			name:      "create allows child topologyName when it matches PCS",
			operation: admissionv1.Create,
			setupNewPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("pcs-child-topology-match")
				pcs.Spec.Template.TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
					TopologyName: "topo-a",
					PackDomain:   grovecorev1alpha1.TopologyDomainZone,
				}
				pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
					TopologyName: "topo-a",
					PackDomain:   grovecorev1alpha1.TopologyDomainHost,
				}
				return pcs
			},
			clusterObjs: []client.Object{createTestClusterTopology()},
		},
		{
			name:      "create rejects child topologyName that differs from PCS",
			operation: admissionv1.Create,
			setupNewPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("pcs-child-topology-mismatch")
				pcs.Spec.Template.TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
					TopologyName: "topo-a",
					PackDomain:   grovecorev1alpha1.TopologyDomainZone,
				}
				pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
					TopologyName: "topo-b",
					PackDomain:   grovecorev1alpha1.TopologyDomainHost,
				}
				return pcs
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.cliques[0].topologyConstraint.topologyName"},
			},
		},
		{
			name:      "update repairs legacy missing child topologyName",
			operation: admissionv1.Update,
			setupOldPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("pcs-repair-missing")
				pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
					PackDomain: grovecorev1alpha1.TopologyDomainHost,
				}
				return pcs
			},
			setupNewPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("pcs-repair-missing")
				pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
					TopologyName: "topo-a",
					PackDomain:   grovecorev1alpha1.TopologyDomainHost,
				}
				return pcs
			},
			clusterObjs: []client.Object{createTestClusterTopology()},
		},
		{
			name:      "update does not treat topologyName-only PCS constraint as repairable legacy shape",
			operation: admissionv1.Update,
			setupOldPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("pcs-repair-child")
				pcs.Spec.Template.TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
					TopologyName: "topo-a",
				}
				return pcs
			},
			setupNewPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("pcs-repair-child")
				pcs.Spec.Template.TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
					TopologyName: "topo-a",
					PackDomain:   grovecorev1alpha1.TopologyDomainZone,
				}
				return pcs
			},
			clusterObjs: []client.Object{createTestClusterTopology()},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.topologyConstraint"},
			},
		},
		{
			name:      "update repair does not allow changing child packDomain",
			operation: admissionv1.Update,
			setupOldPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("pcs-repair-packdomain")
				pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
					PackDomain: grovecorev1alpha1.TopologyDomainHost,
				}
				return pcs
			},
			setupNewPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("pcs-repair-packdomain")
				pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
					TopologyName: "topo-a",
					PackDomain:   grovecorev1alpha1.TopologyDomainRack,
				}
				return pcs
			},
			clusterObjs: []client.Object{createTestClusterTopology()},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.cliques[0].topologyConstraint"},
			},
		},
		{
			name:      "update repair does not allow changing existing topologyName while adding missing packDomain",
			operation: admissionv1.Update,
			setupOldPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("pcs-repair-pcs-packdomain")
				pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
					TopologyName: "topo-a",
				}
				return pcs
			},
			setupNewPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := createTestPodCliqueSet("pcs-repair-pcs-packdomain")
				pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
					TopologyName: "topo-b",
					PackDomain:   grovecorev1alpha1.TopologyDomainHost,
				}
				return pcs
			},
			clusterObjs: []client.Object{createTestClusterTopology()},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.cliques[0].topologyConstraint"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := testutils.CreateDefaultFakeClient(tc.clusterObjs)
			newPCS := tc.setupNewPCS()
			validator := newPCSValidator(newPCS, tc.operation, tasConfig, schedulerConfig, fakeClient)

			var (
				err  error
				errs field.ErrorList
			)
			switch tc.operation {
			case admissionv1.Create:
				_, errs = validator.validate()
				errs = append(errs, validator.validateTopologyConstraintsOnCreate(context.Background())...)
				err = errs.ToAggregate()
			case admissionv1.Update:
				errs = validator.validatePodCliqueSetTemplateSpecUpdate(tc.setupOldPCS(), field.NewPath("spec").Child("template"))
				err = validator.validateUpdate(tc.setupOldPCS())
			default:
				t.Fatalf("unsupported operation %s", tc.operation)
			}

			if tc.errorMatchers != nil {
				require.Error(t, err)
				testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
				return
			}
			require.NoError(t, err)
		})
	}
}

// ---------------------------- Helper Functions ----------------------------

// defaultTASConfig returns a default TAS configuration with TAS disabled.
// This is used for all podcliqueset validation tests since topology constraint
// validation is tested separately in topologyconstraints_v1_test.go.
func defaultTASConfig() groveconfigv1alpha1.TopologyAwareSchedulingConfiguration {
	return groveconfigv1alpha1.TopologyAwareSchedulingConfiguration{
		Enabled: false,
	}
}

// createTestPodCliqueSet creates a basic PodCliqueSet for testing.
func createTestPodCliqueSet(name string) *grovecorev1alpha1.PodCliqueSet {
	return testutils.NewPodCliqueSetBuilder(name, "default", uuid.NewUUID()).
		WithReplicas(1).
		WithTerminationDelay(4 * time.Hour).
		WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder)).
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder("test").
				WithReplicas(1).
				WithRoleName("dummy-role").
				WithMinAvailable(1).
				Build()).
		Build()
}

// createDummyPodCliqueTemplate creates a basic PodCliqueTemplateSpec for testing.
func createDummyPodCliqueTemplate(name string) *grovecorev1alpha1.PodCliqueTemplateSpec {
	return testutils.NewPodCliqueTemplateSpecBuilder(name).
		WithReplicas(1).
		WithRoleName(fmt.Sprintf("dummy-%s-role", name)).
		WithMinAvailable(1).
		Build()
}

// createScalingGroupConfig creates a basic PodCliqueScalingGroupConfig for testing.
func createScalingGroupConfig(name string, cliqueNames []string) grovecorev1alpha1.PodCliqueScalingGroupConfig {
	return grovecorev1alpha1.PodCliqueScalingGroupConfig{
		Name:        name,
		CliqueNames: cliqueNames,
	}
}

func createTestClusterTopology() *grovecorev1alpha1.ClusterTopology {
	return &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{Name: "topo-a"},
		Spec: grovecorev1alpha1.ClusterTopologySpec{
			Levels: []grovecorev1alpha1.TopologyLevel{
				{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
				{Domain: grovecorev1alpha1.TopologyDomainRack, Key: "topology.grove.io/rack"},
				{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
			},
		},
	}
}
