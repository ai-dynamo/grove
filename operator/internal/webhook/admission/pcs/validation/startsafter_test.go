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

package validation

import (
	"testing"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestValidateStartsAfter(t *testing.T) {
	const modelLoaded = "example.com/model-loaded"
	fldPath := field.NewPath("spec", "template", "cliques").Index(1).Child("spec", "startsAfter")
	testCases := []struct {
		description   string
		startsAfter   []grovecorev1beta1.StartupDependency
		errorMatchers []testutils.ErrorMatcher
	}{
		{
			description: "built-in conditions and an empty conditionType accepted",
			startsAfter: []grovecorev1beta1.StartupDependency{
				{Name: "model"},
				{Name: "frontend", ConditionType: string(corev1.ContainersReady)},
			},
		},
		{
			description: "custom condition declared in the parent's readinessGates accepted",
			startsAfter: []grovecorev1beta1.StartupDependency{{Name: "model", ConditionType: modelLoaded}},
		},
		{
			description: "custom condition not declared by the parent rejected",
			startsAfter: []grovecorev1beta1.StartupDependency{{Name: "frontend", ConditionType: modelLoaded}},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: fldPath.Index(0).Child("conditionType").String()},
			},
		},
		{
			description: "DisruptionTarget rejected",
			startsAfter: []grovecorev1beta1.StartupDependency{{Name: "model", ConditionType: string(corev1.DisruptionTarget)}},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: fldPath.Index(0).Child("conditionType").String()},
			},
		},
		{
			description: "conditionType that is not a qualified name rejected",
			startsAfter: []grovecorev1beta1.StartupDependency{{Name: "model", ConditionType: "not a name"}},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: fldPath.Index(0).Child("conditionType").String()},
			},
		},
		{
			description: "empty name rejected",
			startsAfter: []grovecorev1beta1.StartupDependency{{Name: ""}},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeRequired, Field: fldPath.Index(0).Child("name").String()},
			},
		},
		{
			description: "self reference rejected",
			startsAfter: []grovecorev1beta1.StartupDependency{{Name: "worker"}},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: fldPath.Index(0).Child("name").String()},
			},
		},
		{
			description: "duplicate names rejected",
			startsAfter: []grovecorev1beta1.StartupDependency{{Name: "model"}, {Name: "model", ConditionType: modelLoaded}},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeDuplicate, Field: fldPath.Child("name").String()},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			model := createPodCliqueTemplate("model")
			model.Spec.PodSpec.ReadinessGates = []corev1.PodReadinessGate{{ConditionType: modelLoaded}}
			pcs := createTestPodCliqueSet("pcs")
			pcs.Spec.Template.Cliques = []*grovecorev1beta1.PodCliqueTemplateSpec{model, createPodCliqueTemplate("frontend")}
			v := &pcsValidator{pcs: pcs}

			errs := v.validateStartsAfter("worker", tc.startsAfter, fldPath)
			if tc.errorMatchers == nil {
				if len(errs) != 0 {
					t.Fatalf("expected no errors, got %v", errs)
				}
				return
			}
			testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
		})
	}
}
