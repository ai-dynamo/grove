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
	"fmt"
	"os"

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	operatorvalidation "github.com/ai-dynamo/grove/operator/api/config/validation"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

const (
	// ExitSuccess is the exit code indicating that the application exited with no error.
	ExitSuccess = iota
	// ExitErrParseCLIArgs is the exit code indicating that the application exited due to an error parsing CLI arguments.
	ExitErrParseCLIArgs
	// ExitErrLoadOperatorConfig indicates that the application exited due to an error loading the operator configuration.
	ExitErrLoadOperatorConfig
	// ExitErrSynchronizeTopology indicates that the application exited due to an error synchronizing the cluster topology.
	ExitErrSynchronizeTopology
	// ExitErrInitializeManager indicates that the application exited due to an error initializing the manager.
	// This includes registration of controllers and webhooks and setting up probes.
	ExitErrInitializeManager
	// ExitErrInitializeSchedulerBackend indicates that the application exited due to an error initializing the scheduler backend.
	ExitErrInitializeSchedulerBackend
	// ExitErrStart indicates that the application exited due to an error when starting the application.
	ExitErrStart
	// ExitErrMNNVLPrerequisites indicates that the application exited because MNNVL prerequisites are not met.
	ExitErrMNNVLPrerequisites
)

var (
	errParseCLIArgs          = fmt.Errorf("error parsing cli arguments")
	errMissingLaunchOption   = fmt.Errorf("missing required launch option")
	errLoadOperatorConfig    = fmt.Errorf("cannot load %q operator config", apicommonconstants.OperatorName)
	errInvalidOperatorConfig = fmt.Errorf("invalid %q operator config", apicommonconstants.OperatorName)
)

// LaunchOptions defines options for launching the operator.
type LaunchOptions struct {
	// ConfigFile is the path to the operator configuration file.
	ConfigFile string
	// Version indicates whether to print the operator version and exit.
	Version bool
}

// ParseLaunchOptions parses the CLI arguments for the operator.
func ParseLaunchOptions(cliArgs []string) (*LaunchOptions, error) {
	launchOpts := &LaunchOptions{}
	flagSet := pflag.NewFlagSet(apicommonconstants.OperatorName, pflag.ContinueOnError)
	launchOpts.mapFlags(flagSet)
	if err := flagSet.Parse(cliArgs); err != nil {
		return nil, fmt.Errorf("%w: %w", errParseCLIArgs, err)
	}
	if err := launchOpts.validate(); err != nil {
		return nil, err
	}
	return launchOpts, nil
}

// LoadAndValidateOperatorConfig loads and validates the operator configuration from the specified path in the launch options.
func (o *LaunchOptions) LoadAndValidateOperatorConfig() (*configv1alpha1.OperatorConfiguration, error) {
	operatorConfig, err := o.loadOperatorConfig()
	if err != nil {
		return nil, err
	}
	if errs := operatorvalidation.ValidateOperatorConfiguration(operatorConfig); len(errs) > 0 {
		return nil, fmt.Errorf("%w: %w", errInvalidOperatorConfig, errs.ToAggregate())
	}
	return operatorConfig, nil
}

func (o *LaunchOptions) loadOperatorConfig() (*configv1alpha1.OperatorConfiguration, error) {
	// Set up scheme and decoder for operator configuration
	configScheme := runtime.NewScheme()
	if err := configv1alpha1.AddToScheme(configScheme); err != nil {
		return nil, fmt.Errorf("%w: error adding to scheme: %w", errLoadOperatorConfig, err)
	}
	configDecoder := serializer.NewCodecFactory(configScheme).UniversalDecoder()

	// Read configuration file
	operatorConfigBytes, err := os.ReadFile(o.ConfigFile)
	if err != nil {
		return nil, fmt.Errorf("%w: error reading file: %w", errLoadOperatorConfig, err)
	}

	// Decode configuration
	operatorConfig := &configv1alpha1.OperatorConfiguration{}
	if err = runtime.DecodeInto(configDecoder, operatorConfigBytes, operatorConfig); err != nil {
		return nil, fmt.Errorf("%w: error decoding operator config: %w", errLoadOperatorConfig, err)
	}
	return operatorConfig, nil
}

func (o *LaunchOptions) mapFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.ConfigFile, "config", "", "Path to the operator configuration file.")
	fs.BoolVarP(&o.Version, "version", "v", false, "Print the version and exit.")
}

func (o *LaunchOptions) validate() error {
	if len(o.ConfigFile) == 0 && !o.Version {
		return fmt.Errorf("%w: one of version or operator config file must be specified", errMissingLaunchOption)
	}
	if len(o.ConfigFile) > 0 && o.Version {
		return fmt.Errorf("%w: both version and operator config file cannot be specified", errMissingLaunchOption)
	}
	return nil
}
