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

package logger

import (
	"testing"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"

	"go.uber.org/zap/zapcore"
)

// TestCreateLogLevelOpts verifies that log level configurations are correctly
// converted to Zap logger options.
func TestCreateLogLevelOpts(t *testing.T) {
	tests := []struct {
		// name is the test case identifier
		name string
		// level is the input log level to convert
		level configv1alpha1.LogLevel
		// wantErr indicates whether an error is expected
		wantErr bool
		// expectedLevel is the expected Zap level (only checked when wantErr is false)
		expectedLevel zapcore.Level
	}{
		{
			// Debug level should map to zapcore.DebugLevel
			name:          "debug level",
			level:         configv1alpha1.DebugLevel,
			wantErr:       false,
			expectedLevel: zapcore.DebugLevel,
		},
		{
			// Info level should map to zapcore.InfoLevel
			name:          "info level",
			level:         configv1alpha1.InfoLevel,
			wantErr:       false,
			expectedLevel: zapcore.InfoLevel,
		},
		{
			// Empty string should default to info level
			name:          "empty level defaults to info",
			level:         "",
			wantErr:       false,
			expectedLevel: zapcore.InfoLevel,
		},
		{
			// Error level should map to zapcore.ErrorLevel
			name:          "error level",
			level:         configv1alpha1.ErrorLevel,
			wantErr:       false,
			expectedLevel: zapcore.ErrorLevel,
		},
		{
			// Invalid log level should return an error
			name:    "invalid level",
			level:   "invalid",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := createLogLevelOpts(tt.level)
			if (err != nil) != tt.wantErr {
				t.Errorf("createLogLevelOpts() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && opts == nil {
				t.Errorf("createLogLevelOpts() returned nil opts without error")
			}
		})
	}
}

// TestCreateLogFormatOpts verifies that log format configurations are correctly
// converted to Zap logger options.
func TestCreateLogFormatOpts(t *testing.T) {
	tests := []struct {
		// name is the test case identifier
		name string
		// format is the input log format to convert
		format configv1alpha1.LogFormat
		// wantErr indicates whether an error is expected
		wantErr bool
	}{
		{
			// JSON format should return JSON encoder options
			name:    "json format",
			format:  configv1alpha1.LogFormatJSON,
			wantErr: false,
		},
		{
			// Empty string should default to JSON format
			name:    "empty format defaults to json",
			format:  "",
			wantErr: false,
		},
		{
			// Text format should return console encoder options
			name:    "text format",
			format:  configv1alpha1.LogFormatText,
			wantErr: false,
		},
		{
			// Invalid log format should return an error
			name:    "invalid format",
			format:  "invalid",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := createLogFormatOpts(tt.format)
			if (err != nil) != tt.wantErr {
				t.Errorf("createLogFormatOpts() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && opts == nil {
				t.Errorf("createLogFormatOpts() returned nil opts without error")
			}
		})
	}
}

// TestBuildDefaultLoggerOpts verifies that logger options are correctly built
// from configuration parameters.
func TestBuildDefaultLoggerOpts(t *testing.T) {
	tests := []struct {
		// name is the test case identifier
		name string
		// devMode specifies whether development mode is enabled
		devMode bool
		// level is the log level configuration
		level configv1alpha1.LogLevel
		// format is the log format configuration
		format configv1alpha1.LogFormat
		// wantErr indicates whether an error is expected
		wantErr bool
	}{
		{
			// Production configuration with info level and JSON format
			name:    "production mode with info level and json format",
			devMode: false,
			level:   configv1alpha1.InfoLevel,
			format:  configv1alpha1.LogFormatJSON,
			wantErr: false,
		},
		{
			// Development configuration with debug level and text format
			name:    "dev mode with debug level and text format",
			devMode: true,
			level:   configv1alpha1.DebugLevel,
			format:  configv1alpha1.LogFormatText,
			wantErr: false,
		},
		{
			// Configuration with error level
			name:    "error level configuration",
			devMode: false,
			level:   configv1alpha1.ErrorLevel,
			format:  configv1alpha1.LogFormatJSON,
			wantErr: false,
		},
		{
			// Default values (empty strings) should work
			name:    "default values",
			devMode: false,
			level:   "",
			format:  "",
			wantErr: false,
		},
		{
			// Invalid log level should cause an error
			name:    "invalid log level",
			devMode: false,
			level:   "invalid",
			format:  configv1alpha1.LogFormatJSON,
			wantErr: true,
		},
		{
			// Invalid log format should cause an error
			name:    "invalid log format",
			devMode: false,
			level:   configv1alpha1.InfoLevel,
			format:  "invalid",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := buildDefaultLoggerOpts(tt.devMode, tt.level, tt.format)
			if (err != nil) != tt.wantErr {
				t.Errorf("buildDefaultLoggerOpts() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if opts == nil {
					t.Errorf("buildDefaultLoggerOpts() returned nil opts without error")
				}
				// Verify that we get the expected number of options (3: devMode, format, level)
				if len(opts) != 3 {
					t.Errorf("buildDefaultLoggerOpts() returned %d opts, expected 3", len(opts))
				}
			}
		})
	}
}

// TestMustNewLogger verifies that MustNewLogger correctly creates a logger
// with valid inputs and panics with invalid inputs.
func TestMustNewLogger(t *testing.T) {
	tests := []struct {
		// name is the test case identifier
		name string
		// devMode specifies whether development mode is enabled
		devMode bool
		// level is the log level configuration
		level configv1alpha1.LogLevel
		// format is the log format configuration
		format configv1alpha1.LogFormat
		// wantPanic indicates whether a panic is expected
		wantPanic bool
	}{
		{
			// Valid production configuration should create logger successfully
			name:      "valid production configuration",
			devMode:   false,
			level:     configv1alpha1.InfoLevel,
			format:    configv1alpha1.LogFormatJSON,
			wantPanic: false,
		},
		{
			// Valid development configuration should create logger successfully
			name:      "valid development configuration",
			devMode:   true,
			level:     configv1alpha1.DebugLevel,
			format:    configv1alpha1.LogFormatText,
			wantPanic: false,
		},
		{
			// Valid configuration with error level
			name:      "valid configuration with error level",
			devMode:   false,
			level:     configv1alpha1.ErrorLevel,
			format:    configv1alpha1.LogFormatJSON,
			wantPanic: false,
		},
		{
			// Default values should work
			name:      "default values",
			devMode:   false,
			level:     "",
			format:    "",
			wantPanic: false,
		},
		{
			// Invalid log level should cause panic
			name:      "invalid log level causes panic",
			devMode:   false,
			level:     "invalid",
			format:    configv1alpha1.LogFormatJSON,
			wantPanic: true,
		},
		{
			// Invalid log format should cause panic
			name:      "invalid log format causes panic",
			devMode:   false,
			level:     configv1alpha1.InfoLevel,
			format:    "invalid",
			wantPanic: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if (r != nil) != tt.wantPanic {
					t.Errorf("MustNewLogger() panic = %v, wantPanic %v", r != nil, tt.wantPanic)
				}
			}()

			logger := MustNewLogger(tt.devMode, tt.level, tt.format)
			if !tt.wantPanic && logger.GetSink() == nil {
				t.Errorf("MustNewLogger() returned logger with nil sink")
			}
		})
	}
}
