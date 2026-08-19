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

package utils

import (
	"errors"
	"testing"

	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"

	"github.com/stretchr/testify/assert"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

var (
	// TestAPIInternalErr is an API internal server error meant to be used to mimic HTTP response code 500 for tests.
	TestAPIInternalErr = apierrors.NewInternalError(errors.New("fake internal error"))
)

// CheckGroveError checks that an actual error is a Grove error and further checks its underline cause, error code and operation.
func CheckGroveError(t *testing.T, expectedError *groveerr.GroveError, actualErr error) {
	assert.Error(t, expectedError)
	var groveErr *groveerr.GroveError
	assert.True(t, errors.As(actualErr, &groveErr))
	assert.Equal(t, groveErr.Code, expectedError.Code)
	assert.True(t, errors.Is(groveErr.Cause, expectedError.Cause))
	assert.Equal(t, groveErr.Operation, expectedError.Operation)
}
