//go:build e2e

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

package tests

import (
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/utils"
	"github.com/ai-dynamo/grove/operator/internal/diagnostics"
)

// loggerAdapter adapts utils.Logger to implement diagnostics.Logger
type loggerAdapter struct {
	logger *utils.Logger
}

// Info implements diagnostics.Logger
func (l *loggerAdapter) Info(msg string) {
	l.logger.Info(msg)
}

// Infof implements diagnostics.Logger
func (l *loggerAdapter) Infof(format string, args ...any) {
	l.logger.Infof(format, args...)
}

// CollectAllDiagnostics collects and prints all diagnostic information at INFO level.
// This should be called when a test fails, before cleanup runs.
// All output is at INFO level to ensure visibility regardless of log level settings.
func CollectAllDiagnostics(tc TestContext) {
	// Create diagnostic context from test context
	dc := diagnostics.NewDiagnosticContext(tc.Ctx, tc.Clientset, tc.DynamicClient, tc.Namespace)
	dc.OperatorNamespace = setup.OperatorNamespace
	dc.OperatorDeploymentPrefix = setup.OperatorDeploymentName

	// Create console output with logger adapter
	adapter := &loggerAdapter{logger: logger}
	output := diagnostics.NewConsoleOutput(adapter)

	// Collect all diagnostics
	if err := diagnostics.CollectAllDiagnostics(dc, output); err != nil {
		logger.Errorf("Failed to collect diagnostics: %v", err)
	}
}
