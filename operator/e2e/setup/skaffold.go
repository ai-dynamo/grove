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

package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/utils"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// SkaffoldInstallConfig holds configuration for Skaffold installations.
type SkaffoldInstallConfig struct {
	// SkaffoldYAMLPath is the path to the skaffold.yaml file. Required.
	SkaffoldYAMLPath string
	// RestConfig is the Kubernetes REST configuration. Required.
	RestConfig *rest.Config
	// Profiles are the Skaffold profiles to activate (optional).
	Profiles []string
	// PushRepo is the repository to push images to (e.g., "localhost:5001"). Required.
	PushRepo string
	// PullRepo is the repository to pull images from (e.g., "registry:5001"). Required.
	PullRepo string
	// Namespace is the target namespace for deployment (optional, defaults to "default").
	Namespace string
	// Env are environment variables required by skaffold.yaml (e.g., VERSION, LD_FLAGS).
	Env map[string]string
	// Logger is the logger for operations (optional, will use default if nil).
	Logger *utils.Logger
}

// Validate validates the configuration.
func (c *SkaffoldInstallConfig) Validate() error {
	if c.SkaffoldYAMLPath == "" {
		return fmt.Errorf("SkaffoldYAMLPath is required")
	}
	if c.RestConfig == nil {
		return fmt.Errorf("RestConfig is required")
	}
	if c.PushRepo == "" {
		return fmt.Errorf("PushRepo is required")
	}
	if c.PullRepo == "" {
		return fmt.Errorf("PullRepo is required")
	}

	// Set defaults
	if c.Logger == nil {
		c.Logger = utils.NewTestLogger(utils.InfoLevel)
	}
	if c.Env == nil {
		c.Env = make(map[string]string)
	}

	return nil
}

// InstallWithSkaffold builds and deploys using Skaffold CLI. It runs in two phases to
// account for the push registry being different than the pull registry for k3d.
// Note: we're using Skaffold CLI  instead of go libraries because there are
// depdenency conflicts between grove and Skaffold at the time of implentaiton.
func InstallWithSkaffold(ctx context.Context, config *SkaffoldInstallConfig) (func(), error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Resolve to absolute path to ensure it works regardless of current working directory
	absSkaffoldPath, err := filepath.Abs(config.SkaffoldYAMLPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve absolute path for %s: %w", config.SkaffoldYAMLPath, err)
	}

	config.Logger.Debugf("🔧 Installing using Skaffold from: %s", absSkaffoldPath)

	// Create a temporary kubeconfig file
	kubeconfigPath, cleanup, err := writeTemporaryKubeconfig(config.RestConfig, config.Logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary kubeconfig: %w", err)
	}

	// Ensure cleanup is called even if installation fails
	defer func() {
		if err != nil {
			cleanup()
		}
	}()

	// Get the directory containing the skaffold.yaml file
	skaffoldDir := filepath.Dir(absSkaffoldPath)

	// Phase 1: Build and push images to localhost:port
	config.Logger.Debugf("🏗️ Phase 1: Building and pushing images...")
	builtImages, err := runSkaffoldBuild(ctx, absSkaffoldPath, skaffoldDir, kubeconfigPath, config)
	if err != nil {
		return cleanup, fmt.Errorf("skaffold build failed: %w", err)
	}

	// Phase 2: Deploy using registry:port for image pulls
	config.Logger.Debugf("🚀 Phase 2: Deploying with registry configuration...")
	if err = runSkaffoldDeploy(ctx, absSkaffoldPath, skaffoldDir, kubeconfigPath, config, builtImages); err != nil {
		return cleanup, fmt.Errorf("skaffold deploy failed: %w", err)
	}

	config.Logger.Debug("✅ Skaffold installation completed successfully")
	return cleanup, nil
}

// runSkaffoldBuild runs skaffold build to build and push images to PushRepo
// Returns a map of image names to their full tags with digests
func runSkaffoldBuild(ctx context.Context, absSkaffoldPath, skaffoldDir, kubeconfigPath string, config *SkaffoldInstallConfig) (map[string]string, error) {
	args := []string{"build", "--quiet", "--output={{json .}}"}

	// Add default repo for build (push repository)
	args = append(args, "--default-repo", config.PushRepo)

	// Add profiles if specified
	for _, profile := range config.Profiles {
		args = append(args, "--profile", profile)
	}

	// Add the skaffold.yaml path
	args = append(args, "-f", absSkaffoldPath)

	// Add timeout to prevent indefinite hangs (normal builds should complete in <10 minutes)
	buildCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(buildCtx, "skaffold", args...)
	cmd.Dir = skaffoldDir

	// Set up environment variables
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))

	// Add build-specific environment variables
	for k, v := range config.Env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	// cmd.Stdout will receive json output from skaffold on completion
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	// cmd.Stderr is logging, it'll get passed on at debug level only
	cmd.Stderr = config.Logger.WriterLevel(utils.DebugLevel)

	config.Logger.Debugf("   Running: skaffold %v", args)
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	// Parse the JSON output to get built images
	var buildOutput struct {
		Builds []struct {
			ImageName string `json:"imageName"`
			Tag       string `json:"tag"`
		} `json:"builds"`
	}

	if err := json.Unmarshal(stdout.Bytes(), &buildOutput); err != nil {
		return nil, fmt.Errorf("failed to parse build output: %w", err)
	}

	// Create map of image names to tags
	images := make(map[string]string)
	for _, build := range buildOutput.Builds {
		images[build.ImageName] = build.Tag
		config.Logger.Debugf("   Built: %s -> %s", build.ImageName, build.Tag)
	}

	return images, nil
}

// runSkaffoldDeploy runs skaffold deploy with registry configuration
func runSkaffoldDeploy(ctx context.Context, absSkaffoldPath, skaffoldDir, kubeconfigPath string, config *SkaffoldInstallConfig, builtImages map[string]string) error {
	args := []string{"deploy", "--status-check=false"}

	// Add namespace if specified
	if config.Namespace != "" {
		args = append(args, "--namespace", config.Namespace)
	}

	// Add built images with PullRepo instead of PushRepo
	for imageName, imageTag := range builtImages {
		// Convert PushRepo in image tag to PullRepo (e.g., localhost:5001/image@sha256:... to registry:5001/image@sha256:...)
		pullImageTag := strings.Replace(imageTag, config.PushRepo, config.PullRepo, 1)
		args = append(args, "--images", fmt.Sprintf("%s=%s", imageName, pullImageTag))
		config.Logger.Debugf("   Image mapping: %s -> %s", imageName, pullImageTag)
	}

	// Add profiles if specified
	for _, profile := range config.Profiles {
		args = append(args, "--profile", profile)
	}

	// Add the skaffold.yaml path
	args = append(args, "-f", absSkaffoldPath)

	cmd := exec.CommandContext(ctx, "skaffold", args...)
	cmd.Dir = skaffoldDir

	// Set up environment variables
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))

	// Add deploy-specific environment variables
	for k, v := range config.Env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	// Override IMAGE_REPO and CONTAINER_REGISTRY for deploy phase to use PullRepo
	cmd.Env = append(cmd.Env, fmt.Sprintf("IMAGE_REPO=%s/grove-operator", config.PullRepo))
	cmd.Env = append(cmd.Env, fmt.Sprintf("CONTAINER_REGISTRY=%s", config.PullRepo))

	// log cmd.Stdout and cmd.Stderr at debug level only
	cmd.Stdout = config.Logger.WriterLevel(utils.DebugLevel)
	cmd.Stderr = config.Logger.WriterLevel(utils.DebugLevel)

	config.Logger.Debugf("   Running: skaffold %v", args)
	return cmd.Run()
}

// writeTemporaryKubeconfig converts a rest.Config to a kubeconfig file and writes it to a temporary location.
// Returns the path to the temporary file and a cleanup function.
func writeTemporaryKubeconfig(restConfig *rest.Config, logger *utils.Logger) (string, func(), error) {
	// Create a temporary file for the kubeconfig
	tmpFile, err := os.CreateTemp("", "kubeconfig-*.yaml")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temporary file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()

	cleanup := func() {
		logger.Debugf("🗑️ Cleaning up temporary kubeconfig: %s", tmpPath)
		if err := os.Remove(tmpPath); err != nil {
			logger.Warnf("Failed to remove temporary kubeconfig %s: %v", tmpPath, err)
		}
	}

	// Convert rest.Config to kubeconfig API config
	kubeconfig := &clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"default-cluster": {
				Server:                   restConfig.Host,
				CertificateAuthorityData: restConfig.CAData,
				InsecureSkipTLSVerify:    restConfig.Insecure,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"default-context": {
				Cluster:  "default-cluster",
				AuthInfo: "default-user",
			},
		},
		CurrentContext: "default-context",
	}

	// Set up authentication
	authInfo := &clientcmdapi.AuthInfo{}

	if restConfig.CertData != nil || restConfig.CertFile != "" {
		authInfo.ClientCertificateData = restConfig.CertData
		authInfo.ClientCertificate = restConfig.CertFile
	}

	if restConfig.KeyData != nil || restConfig.KeyFile != "" {
		authInfo.ClientKeyData = restConfig.KeyData
		authInfo.ClientKey = restConfig.KeyFile
	}

	if restConfig.BearerToken != "" {
		authInfo.Token = restConfig.BearerToken
	}

	if restConfig.Username != "" {
		authInfo.Username = restConfig.Username
		authInfo.Password = restConfig.Password
	}

	kubeconfig.AuthInfos = map[string]*clientcmdapi.AuthInfo{
		"default-user": authInfo,
	}

	// Write the kubeconfig to the temporary file
	if err := clientcmd.WriteToFile(*kubeconfig, tmpPath); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to write kubeconfig: %w", err)
	}

	logger.Debugf("📄 Wrote temporary kubeconfig to: %s", tmpPath)
	return tmpPath, cleanup, nil
}
