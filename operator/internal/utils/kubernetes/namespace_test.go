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
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-dynamo/grove/operator/internal/constants"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
)

func TestOperatorServiceName(t *testing.T) {
	t.Run("defaults when the environment variable is not set", func(t *testing.T) {
		t.Setenv(constants.EnvVarServiceName, "")
		assert.Equal(t, constants.DefaultServiceName, OperatorServiceName())
	})
	t.Run("environment variable is used", func(t *testing.T) {
		t.Setenv(constants.EnvVarServiceName, " my-grove ")
		assert.Equal(t, "my-grove", OperatorServiceName())
	})
}

func TestNamespaceFromFile(t *testing.T) {
	testCases := []struct {
		description string
		content     *string
		expected    string
		errContains string
	}{
		{
			description: "file does not exist",
			errContains: "no such file",
		},
		{
			description: "namespace is returned",
			content:     ptr.To("grove-system"),
			expected:    "grove-system",
		},
		{
			description: "surrounding whitespace is trimmed",
			content:     ptr.To("  grove-system\n"),
			expected:    "grove-system",
		},
		{
			description: "empty file is an error",
			content:     ptr.To(""),
			errContains: "is empty",
		},
		{
			description: "whitespace-only file is an error",
			content:     ptr.To(" \n"),
			errContains: "is empty",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "namespace")
			if tc.content != nil {
				require.NoError(t, os.WriteFile(path, []byte(*tc.content), 0o600))
			}
			namespace, err := namespaceFromFile(path)
			if tc.errContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.expected, namespace)
		})
	}
}
