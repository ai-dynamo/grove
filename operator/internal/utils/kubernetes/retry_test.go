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

package kubernetes

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestIsRetriableAPIError verifies that optimistic-lock conflicts and transient server-side
// failures are reported as retriable, while permanent errors, non-API errors and nil are not.
func TestIsRetriableAPIError(t *testing.T) {
	gr := schema.GroupResource{Group: "grove.io", Resource: "podcliques"}
	gk := schema.GroupKind{Group: "grove.io", Kind: "PodClique"}
	cause := errors.New("boom")

	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{name: "conflict is retriable", err: apierrors.NewConflict(gr, "x", cause), expected: true},
		{name: "server timeout is retriable", err: apierrors.NewServerTimeout(gr, "get", 1), expected: true},
		{name: "timeout is retriable", err: apierrors.NewTimeoutError("timed out", 1), expected: true},
		{name: "too many requests is retriable", err: apierrors.NewTooManyRequests("slow down", 1), expected: true},
		{name: "service unavailable is retriable", err: apierrors.NewServiceUnavailable("unavailable"), expected: true},
		{name: "internal error is retriable", err: apierrors.NewInternalError(cause), expected: true},
		{name: "not found is not retriable", err: apierrors.NewNotFound(gr, "x"), expected: false},
		{name: "bad request is not retriable", err: apierrors.NewBadRequest("bad"), expected: false},
		{name: "forbidden is not retriable", err: apierrors.NewForbidden(gr, "x", cause), expected: false},
		{name: "invalid is not retriable", err: apierrors.NewInvalid(gk, "x", nil), expected: false},
		{name: "non-API error is not retriable", err: errors.New("plain error"), expected: false},
		{name: "nil is not retriable", err: nil, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := IsRetriableAPIError(tt.err)
			assert.Equal(t, tt.expected, actual)
		})
	}
}
