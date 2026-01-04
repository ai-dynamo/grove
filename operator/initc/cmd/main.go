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

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/initc/cmd/opts"
	"github.com/ai-dynamo/grove/operator/initc/internal"
	"github.com/ai-dynamo/grove/operator/internal/logger"
)

var (
	log = logger.MustNewLogger(false, configv1alpha1.InfoLevel, configv1alpha1.LogFormatJSON).WithName("grove-initc")
)

func main() {
	ctx := setupSignalHandler()

	config, err := opts.InitializeCLIOptions()
	if err != nil {
		log.Error(err, "Failed to generate configuration for the init container from the flags")
		os.Exit(1)
	}

	log.Info("Starting grove init container")

	podCliqueDependencies, err := config.GetPodCliqueDependencies()
	if err != nil {
		log.Error(err, "Failed to parse CLI input")
		os.Exit(1)
	}

	podCliqueState, err := internal.NewPodCliqueState(podCliqueDependencies, log)
	if err != nil {
		os.Exit(1)
	}

	if err = podCliqueState.WaitForReady(ctx, log); err != nil {
		log.Error(err, "Failed to wait for all parent PodCliques")
		os.Exit(1)
	}

	log.Info("Successfully waited for all parent PodCliques to start up")
}

// setupSignalHandler sets up the context for the application. Handles the SIGTERM and SIGINT signals.
// The returned context gets cancelled by the first signal. The second signal causes the program with exit code 1.
func setupSignalHandler() context.Context {
	ctx, cancel := context.WithCancel(context.Background())

	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-ch
		cancel()
		<-ch
		os.Exit(1)
	}()

	return ctx
}
