// /*
// Copyright 2024 The Grove Authors.
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
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
)

// TestValidateEnumType tests validation of enum values against allowed sets.
func TestValidateEnumType(t *testing.T) {
	fldPath := field.NewPath("spec", "testField")

	tests := []struct {
		// name identifies this test case
		name string
		// value is the pointer to the value being validated
		value *string
		// allowedValues is the set of valid enum values
		allowedValues sets.Set[string]
		// expectError indicates whether validation should fail
		expectError bool
		// errorType describes the expected error type (Required or Invalid)
		errorType field.ErrorType
	}{
		{
			name:          "nil value returns Required error",
			value:         nil,
			allowedValues: sets.New("option1", "option2"),
			expectError:   true,
			errorType:     field.ErrorTypeRequired,
		},
		{
			name:          "valid value passes validation",
			value:         ptr.To("option1"),
			allowedValues: sets.New("option1", "option2"),
			expectError:   false,
		},
		{
			name:          "invalid value returns Invalid error",
			value:         ptr.To("option3"),
			allowedValues: sets.New("option1", "option2"),
			expectError:   true,
			errorType:     field.ErrorTypeInvalid,
		},
		{
			name:          "empty string value in allowed set passes",
			value:         ptr.To(""),
			allowedValues: sets.New("", "option1"),
			expectError:   false,
		},
		{
			name:          "empty string value not in allowed set fails",
			value:         ptr.To(""),
			allowedValues: sets.New("option1", "option2"),
			expectError:   true,
			errorType:     field.ErrorTypeInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateEnumType(tt.value, tt.allowedValues, fldPath)
			if tt.expectError {
				assert.NotEmpty(t, errs)
				assert.Equal(t, tt.errorType, errs[0].Type)
			} else {
				assert.Empty(t, errs)
			}
		})
	}
}

// TestValidateEnumTypeInt tests validation of integer enum values.
func TestValidateEnumTypeInt(t *testing.T) {
	fldPath := field.NewPath("spec", "testField")

	tests := []struct {
		// name identifies this test case
		name string
		// value is the pointer to the integer value being validated
		value *int
		// allowedValues is the set of valid enum values
		allowedValues sets.Set[int]
		// expectError indicates whether validation should fail
		expectError bool
		// errorType describes the expected error type
		errorType field.ErrorType
	}{
		{
			name:          "nil integer value returns Required error",
			value:         nil,
			allowedValues: sets.New(1, 2, 3),
			expectError:   true,
			errorType:     field.ErrorTypeRequired,
		},
		{
			name:          "valid integer value passes validation",
			value:         ptr.To(2),
			allowedValues: sets.New(1, 2, 3),
			expectError:   false,
		},
		{
			name:          "invalid integer value returns Invalid error",
			value:         ptr.To(5),
			allowedValues: sets.New(1, 2, 3),
			expectError:   true,
			errorType:     field.ErrorTypeInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateEnumType(tt.value, tt.allowedValues, fldPath)
			if tt.expectError {
				assert.NotEmpty(t, errs)
				assert.Equal(t, tt.errorType, errs[0].Type)
			} else {
				assert.Empty(t, errs)
			}
		})
	}
}

// TestValidateNonNilField tests validation of non-nil pointer fields.
func TestValidateNonNilField(t *testing.T) {
	fldPath := field.NewPath("spec", "testField")

	tests := []struct {
		// name identifies this test case
		name string
		// value is the pointer being validated
		value *string
		// expectError indicates whether validation should fail
		expectError bool
	}{
		{
			name:        "nil pointer returns Required error",
			value:       nil,
			expectError: true,
		},
		{
			name:        "non-nil pointer passes validation",
			value:       ptr.To("value"),
			expectError: false,
		},
		{
			name:        "non-nil pointer with empty string passes validation",
			value:       ptr.To(""),
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateNonNilField(tt.value, fldPath)
			if tt.expectError {
				assert.NotEmpty(t, errs)
				assert.Equal(t, field.ErrorTypeRequired, errs[0].Type)
			} else {
				assert.Empty(t, errs)
			}
		})
	}
}

// TestValidateNonEmptyStringField tests validation of non-empty string fields.
func TestValidateNonEmptyStringField(t *testing.T) {
	fldPath := field.NewPath("spec", "testField")

	tests := []struct {
		// name identifies this test case
		name string
		// value is the string being validated
		value string
		// expectError indicates whether validation should fail
		expectError bool
	}{
		{
			name:        "empty string returns Required error",
			value:       "",
			expectError: true,
		},
		{
			name:        "non-empty string passes validation",
			value:       "test-value",
			expectError: false,
		},
		{
			name:        "whitespace-only string returns Required error",
			value:       "   ",
			expectError: true,
		},
		{
			name:        "string with content passes validation",
			value:       "  valid  ",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateNonEmptyStringField(tt.value, fldPath)
			if tt.expectError {
				assert.NotEmpty(t, errs)
				assert.Equal(t, field.ErrorTypeRequired, errs[0].Type)
			} else {
				assert.Empty(t, errs)
			}
		})
	}
}

// TestSliceMustHaveUniqueElements tests validation for duplicate elements in string slices.
func TestSliceMustHaveUniqueElements(t *testing.T) {
	fldPath := field.NewPath("spec", "testField")

	tests := []struct {
		// name identifies this test case
		name string
		// slice is the string slice being validated
		slice []string
		// expectError indicates whether validation should fail
		expectError bool
		// expectedDuplicates is the list of duplicate values expected in the error
		expectedDuplicates []string
	}{
		{
			name:        "slice with no duplicates passes validation",
			slice:       []string{"a", "b", "c"},
			expectError: false,
		},
		{
			name:               "slice with one duplicate returns Duplicate error",
			slice:              []string{"a", "b", "a"},
			expectError:        true,
			expectedDuplicates: []string{"a"},
		},
		{
			name:               "slice with multiple duplicates returns Duplicate error",
			slice:              []string{"a", "b", "a", "c", "b"},
			expectError:        true,
			expectedDuplicates: []string{"a", "b"},
		},
		{
			name:        "empty slice passes validation",
			slice:       []string{},
			expectError: false,
		},
		{
			name:        "nil slice passes validation",
			slice:       nil,
			expectError: false,
		},
		{
			name:        "slice with single element passes validation",
			slice:       []string{"a"},
			expectError: false,
		},
		{
			name:               "slice with all duplicates returns Duplicate error",
			slice:              []string{"a", "a", "a"},
			expectError:        true,
			expectedDuplicates: []string{"a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := sliceMustHaveUniqueElements(tt.slice, fldPath)
			if tt.expectError {
				assert.NotEmpty(t, errs)
				assert.Equal(t, field.ErrorTypeDuplicate, errs[0].Type)
				if len(tt.expectedDuplicates) > 0 {
					badValues, ok := errs[0].BadValue.([]string)
					if assert.True(t, ok, "expected BadValue to be []string") {
						assert.ElementsMatch(t, tt.expectedDuplicates, badValues)
					}
				}
			} else {
				assert.Empty(t, errs)
			}
		})
	}
}
