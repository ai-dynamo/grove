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

package controller

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"
	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"
	"github.com/ai-dynamo/grove/operator/internal/controller/cert"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	"github.com/ai-dynamo/grove/operator/internal/webhook"

	"github.com/go-logr/logr"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrlmetricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	ctrlwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"
)

var (
	waitTillWebhookCertsReady    = cert.WaitTillWebhookCertsReady
	waitTillWebhookServerStarted = waitForWebhookServer
	registerControllersWithMgr   = RegisterControllers
	registerWebhooksWithMgr      = webhook.Register
)

// CreateManager creates the manager.
func CreateManager(operatorCfg *configv1beta1.OperatorConfiguration) (ctrl.Manager, error) {
	return ctrl.NewManager(getRestConfig(operatorCfg), createManagerOptions(operatorCfg))
}

// RegisterControllersAndWebhooks adds all the controllers and webhooks to the controller-manager using the passed in Config.
// Webhooks are registered first and served before the controllers are added, because the controllers read the
// converted kinds through the conversion webhook while they are stored at another version. beforeControllers, if
// set, runs once the webhook server is serving and before any controller is added.
func RegisterControllersAndWebhooks(ctx context.Context, mgr ctrl.Manager, logger logr.Logger, operatorCfg *configv1beta1.OperatorConfiguration, certsReady chan struct{}, schedRegistry scheduler.Registry, beforeControllers func(context.Context) error) error {
	waitTillWebhookCertsReady(logger, certsReady)
	if err := registerWebhooksWithMgr(mgr, operatorCfg, schedRegistry); err != nil {
		return err
	}
	if err := waitTillWebhookServerStarted(ctx, mgr); err != nil {
		return err
	}
	if beforeControllers != nil {
		if err := beforeControllers(ctx); err != nil {
			return err
		}
	}
	return registerControllersWithMgr(mgr, operatorCfg, schedRegistry)
}

// waitForWebhookServer blocks until the manager's webhook server accepts TLS connections.
func waitForWebhookServer(ctx context.Context, mgr ctrl.Manager) error {
	started := mgr.GetWebhookServer().StartedChecker()
	if err := wait.PollUntilContextCancel(ctx, 200*time.Millisecond, true, func(context.Context) (bool, error) {
		return started(&http.Request{}) == nil, nil
	}); err != nil {
		return fmt.Errorf("webhook server did not start: %w", err)
	}
	return nil
}

// SetupHealthAndReadinessEndpoints sets up the health and readiness endpoints for the operator.
func SetupHealthAndReadinessEndpoints(mgr ctrl.Manager, webhookCertsReadyCh chan struct{}) error {
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("could not setup health check :%w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", func(req *http.Request) error {
		select {
		case <-webhookCertsReadyCh:
			return mgr.GetWebhookServer().StartedChecker()(req)
		default:
			return errors.New("certificates are not ready yet")
		}
	}); err != nil {
		return fmt.Errorf("could not setup ready check :%w", err)
	}
	return nil
}

// createManagerOptions constructs controller-runtime Manager options from operator configuration.
func createManagerOptions(operatorCfg *configv1beta1.OperatorConfiguration) ctrl.Options {
	opts := ctrl.Options{
		Scheme:                  groveclientscheme.Scheme,
		GracefulShutdownTimeout: ptr.To(5 * time.Second),
		Cache:                   cacheOptions(),
		Metrics: ctrlmetricsserver.Options{
			BindAddress: net.JoinHostPort(operatorCfg.Server.Metrics.BindAddress, strconv.Itoa(operatorCfg.Server.Metrics.Port)),
		},
		HealthProbeBindAddress:        net.JoinHostPort(operatorCfg.Server.HealthProbes.BindAddress, strconv.Itoa(operatorCfg.Server.HealthProbes.Port)),
		LeaderElection:                operatorCfg.LeaderElection.Enabled,
		LeaderElectionID:              operatorCfg.LeaderElection.ResourceName,
		LeaderElectionResourceLock:    operatorCfg.LeaderElection.ResourceLock,
		LeaderElectionReleaseOnCancel: true,
		LeaseDuration:                 &operatorCfg.LeaderElection.LeaseDuration.Duration,
		RenewDeadline:                 &operatorCfg.LeaderElection.RenewDeadline.Duration,
		RetryPeriod:                   &operatorCfg.LeaderElection.RetryPeriod.Duration,
		Controller: ctrlconfig.Controller{
			RecoverPanic: ptr.To(true),
		},
		WebhookServer: ctrlwebhook.NewServer(ctrlwebhook.Options{
			Host:    operatorCfg.Server.Webhooks.BindAddress,
			Port:    operatorCfg.Server.Webhooks.Port,
			CertDir: operatorCfg.Server.Webhooks.ServerCertDir,
		}),
	}
	if operatorCfg.Debugging != nil {
		if ptr.Deref(operatorCfg.Debugging.EnableProfiling, false) {
			opts.PprofBindAddress = net.JoinHostPort(
				*operatorCfg.Debugging.PprofBindHost,
				strconv.Itoa(*operatorCfg.Debugging.PprofBindPort),
			)
		}
	}
	return opts
}

// cacheOptions returns cache configuration that restricts informers for shared
// core types to only grove-managed resources via label selectors.
// Grove CRDs are not filtered because all instances are grove-managed by definition.
func cacheOptions() cache.Options {
	managedByGrove := cache.ByObject{
		Label: labels.SelectorFromSet(labels.Set{
			apicommon.LabelManagedByKey: apicommon.LabelManagedByValue,
		}),
	}
	return cache.Options{
		ByObject: map[client.Object]cache.ByObject{
			&corev1.Pod{}:                            managedByGrove,
			&corev1.ServiceAccount{}:                 managedByGrove,
			&corev1.Service{}:                        managedByGrove,
			&rbacv1.Role{}:                           managedByGrove,
			&rbacv1.RoleBinding{}:                    managedByGrove,
			&autoscalingv2.HorizontalPodAutoscaler{}: managedByGrove,
		},
	}
}

// getRestConfig creates a Kubernetes REST config with customized client connection settings.
func getRestConfig(operatorCfg *configv1beta1.OperatorConfiguration) *rest.Config {
	restCfg := ctrl.GetConfigOrDie()
	if operatorCfg != nil {
		restCfg.Burst = operatorCfg.ClientConnection.Burst
		restCfg.QPS = operatorCfg.ClientConnection.QPS
		restCfg.AcceptContentTypes = operatorCfg.ClientConnection.AcceptContentTypes
		restCfg.ContentType = operatorCfg.ClientConnection.ContentType
	}
	return restCfg
}
