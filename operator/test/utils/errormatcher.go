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

package utils

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// ErrorMatcher matches the errors contained within field.ErrorList
type ErrorMatcher struct {
	// Field is the value of the value of field.Error.Field.
	Field string
	// ErrorType is the value of field.Error.Type to be compared.
	ErrorType field.ErrorType
}

// Matches matches the errorType to the passed field.ErrorList
func (m ErrorMatcher) Matches(errs field.ErrorList) bool {
	for _, err := range errs {
		if err.Field == m.Field && err.Type == m.ErrorType {
			return true
		}
	}
	return false
}

// AssertErrorMatches asserts that the errs contained within field.ErrorList with all the passed `expectedErrorMatchers`.
func AssertErrorMatches(t *testing.T, errs field.ErrorList, expectedErrorMatchers []ErrorMatcher) {
	unmatched := make([]ErrorMatcher, 0, len(expectedErrorMatchers))
	for _, matcher := range expectedErrorMatchers {
		if !matcher.Matches(errs) {
			unmatched = append(unmatched, matcher)
		}
	}
	assert.True(t, len(unmatched) == 0, aggregateMatchingErrorMessage(unmatched, errs))
}

func aggregateMatchingErrorMessage(unmatched []ErrorMatcher, errs field.ErrorList) string {
	var sb strings.Builder
	sb.WriteString("Expected error to consist of:\n")
	for _, um := range unmatched {
		fmt.Fprintf(&sb, "  Field: %s, Type: %s\n", um.Field, um.ErrorType)
	}
	sb.WriteString("But got errors:\n")
	for _, err := range errs {
		fmt.Fprintf(&sb, "  Field: %s, Type: %s\n", err.Field, err.Type)
	}
	return sb.String()
}
