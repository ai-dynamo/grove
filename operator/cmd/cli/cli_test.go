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

package cli

import (
	"errors"
	"testing"

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseLaunchOptions tests the parsing of CLI arguments into LaunchOptions.
func TestParseLaunchOptions(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		expected      *LaunchOptions
		expectedError error
	}{
		{
			name: "valid config file",
			args: []string{"--config", "testdata/valid-config.yaml"},
			expected: &LaunchOptions{
				ConfigFile: "testdata/valid-config.yaml",
			},
		},
		{
			name:          "no CLI args",
			args:          []string{},
			expected:      nil,
			expectedError: errMissingLaunchOption,
		},
		{
			name:          "unknown CLI flag",
			args:          []string{"--unknown-flag", "value"},
			expected:      nil,
			expectedError: errParseCLIArgs,
		},
		{
			name:          "invalid CLI flag format",
			args:          []string{"---config", "test.yaml"},
			expected:      nil,
			expectedError: errParseCLIArgs,
		},
		{
			name:          "no value specified for config-file flag",
			args:          []string{"--config"},
			expected:      nil,
			expectedError: errParseCLIArgs,
		},
		{
			name: "multiple config_file flags", // last one should take precedence
			args: []string{"--config", "first.yaml", "--config", "second.yaml"},
			expected: &LaunchOptions{
				ConfigFile: "second.yaml",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opts, err := ParseLaunchOptions(test.args)

			if test.expectedError != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, test.expectedError),
					"expected error to wrap %v, got %v", test.expectedError, err)
			} else {
				require.NoError(t, err)
				require.NotNil(t, opts)
				assert.Equal(t, test.expected.ConfigFile, opts.ConfigFile)
			}
		})
	}
}

// TestMapFlags tests that flags are properly mapped to the LaunchOptions struct.
func TestMapFlags(t *testing.T) {
	opts := &LaunchOptions{}
	flagSet := pflag.NewFlagSet(apicommonconstants.OperatorName, pflag.ContinueOnError)
	opts.mapFlags(flagSet)

	// Verify the config-file flag was registered
	flag := flagSet.Lookup("config")
	require.NotNil(t, flag, "config flag should be registered")
	assert.Equal(t, "", flag.DefValue, "config should have empty default value")
	assert.Equal(t, "string", flag.Value.Type(), "config should be a string flag")
}

// TestLoadAndValidateOperatorConfig tests loading and validating operator configuration.
func TestLoadAndValidateOperatorConfig(t *testing.T) {
	tests := []struct {
		name          string
		configFile    string
		expectedError error
		validateFunc  func(t *testing.T, config *configv1alpha1.OperatorConfiguration)
	}{
		{
			name:       "valid config",
			configFile: "testdata/valid-config.yaml",
			validateFunc: func(t *testing.T, config *configv1alpha1.OperatorConfiguration) {
				require.NotNil(t, config)
				assert.Equal(t, configv1alpha1.LogLevel("info"), config.LogLevel)
				assert.Equal(t, configv1alpha1.LogFormat("json"), config.LogFormat)
				assert.Equal(t, float32(100), config.ClientConnection.QPS)
				assert.Equal(t, 150, config.ClientConnection.Burst)
				assert.Equal(t, 9443, config.Server.Webhooks.Port)
				assert.NotNil(t, config.Server.HealthProbes)
				assert.Equal(t, 9444, config.Server.HealthProbes.Port)
				assert.NotNil(t, config.Server.Metrics)
				assert.Equal(t, 9445, config.Server.Metrics.Port)
				assert.NotNil(t, config.Controllers.PodCliqueSet.ConcurrentSyncs)
				assert.Equal(t, 3, *config.Controllers.PodCliqueSet.ConcurrentSyncs)
				assert.False(t, config.MNNVL.Enabled, "MNNVL should be disabled by default")
			},
		},
		{
			name:       "valid config with MNNVL enabled",
			configFile: "testdata/valid-config-mnnvl-enabled.yaml",
			validateFunc: func(t *testing.T, config *configv1alpha1.OperatorConfiguration) {
				require.NotNil(t, config)
				assert.True(t, config.MNNVL.Enabled, "MNNVL should be enabled")
			},
		},
		{
			name:          "invalid operator config",
			configFile:    "testdata/invalid-config.yaml",
			expectedError: errInvalidOperatorConfig,
		},
		{
			name:          "non existent config file",
			configFile:    "testdata/nonexistent.yaml",
			expectedError: errLoadOperatorConfig,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opts := &LaunchOptions{
				ConfigFile: test.configFile,
			}

			config, err := opts.LoadAndValidateOperatorConfig()

			if test.expectedError != nil {
				require.Error(t, err)
				if test.expectedError != nil {
					assert.True(t, errors.Is(err, test.expectedError),
						"expected error to wrap %v, got %v", test.expectedError, err)
				}
				assert.Nil(t, config)
			} else {
				require.NoError(t, err)
				require.NotNil(t, config)
				if test.validateFunc != nil {
					test.validateFunc(t, config)
				}
			}
		})
	}
}
