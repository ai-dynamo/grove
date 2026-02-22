// /*
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
// */

package v1alpha1

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const (
	defaultLeaderElectionResourceLock    = "leases"
	defaultLeaderElectionResourceName    = "grove-operator-leader-election"
	defaultWebhookServerTLSServerCertDir = "/etc/grove-operator/webhook-certs"
)

// SetDefaults_ClientConnectionConfiguration sets defaults for the k8s client connection.
func SetDefaults_ClientConnectionConfiguration(clientConnConfig *ClientConnectionConfiguration) {
	if clientConnConfig.QPS == 0.0 {
		clientConnConfig.QPS = 100.0
	}
	if clientConnConfig.Burst == 0 {
		clientConnConfig.Burst = 120
	}
}

// SetDefaults_LeaderElectionConfiguration sets defaults for the leader election of the Grove operator.
func SetDefaults_LeaderElectionConfiguration(leaderElectionConfig *LeaderElectionConfiguration) {
	zero := metav1.Duration{}
	if leaderElectionConfig.LeaseDuration == zero {
		leaderElectionConfig.LeaseDuration = metav1.Duration{Duration: 15 * time.Second}
	}
	if leaderElectionConfig.RenewDeadline == zero {
		leaderElectionConfig.RenewDeadline = metav1.Duration{Duration: 10 * time.Second}
	}
	if leaderElectionConfig.RetryPeriod == zero {
		leaderElectionConfig.RetryPeriod = metav1.Duration{Duration: 2 * time.Second}
	}
	if leaderElectionConfig.ResourceLock == "" {
		leaderElectionConfig.ResourceLock = defaultLeaderElectionResourceLock
	}
	if leaderElectionConfig.ResourceName == "" {
		leaderElectionConfig.ResourceName = defaultLeaderElectionResourceName
	}
}

// SetDefaults_OperatorConfiguration sets defaults for the configuration of the Grove operator.
func SetDefaults_OperatorConfiguration(operatorConfig *OperatorConfiguration) {
	if operatorConfig.LogLevel == "" {
		operatorConfig.LogLevel = "info"
	}
	if operatorConfig.LogFormat == "" {
		operatorConfig.LogFormat = "json"
	}
	SetDefaults_SchedulerConfiguration(&operatorConfig.Scheduler)
}

// SetDefaults_SchedulerConfiguration sets defaults for scheduler configuration.
// When Profiles is empty, defaults to a single profile for kube-scheduler as default.
func SetDefaults_SchedulerConfiguration(cfg *SchedulerConfiguration) {
	if len(cfg.Profiles) == 0 {
		cfg.Profiles = []SchedulerProfile{
			{Name: SchedulerNameKube, Default: true},
		}
	}
}

// SetDefaults_ServerConfiguration sets defaults for the server configuration.
func SetDefaults_ServerConfiguration(serverConfig *ServerConfiguration) {
	if serverConfig.Webhooks.Port == 0 {
		serverConfig.Webhooks.Port = 2750
	}

	if serverConfig.Webhooks.ServerCertDir == "" {
		serverConfig.Webhooks.ServerCertDir = defaultWebhookServerTLSServerCertDir
	}

	if serverConfig.Webhooks.SecretName == "" {
		serverConfig.Webhooks.SecretName = DefaultWebhookSecretName
	}

	if serverConfig.Webhooks.CertProvisionMode == "" {
		serverConfig.Webhooks.CertProvisionMode = CertProvisionModeAuto
	}

	if serverConfig.HealthProbes == nil {
		serverConfig.HealthProbes = &Server{}
	}
	if serverConfig.HealthProbes.Port == 0 {
		serverConfig.HealthProbes.Port = 2751
	}

	if serverConfig.Metrics == nil {
		serverConfig.Metrics = &Server{}
	}
	if serverConfig.Metrics.Port == 0 {
		serverConfig.Metrics.Port = 2752
	}
}

// SetDefaults_PodCliqueSetControllerConfiguration sets defaults for the PodCliqueSetControllerConfiguration.
func SetDefaults_PodCliqueSetControllerConfiguration(obj *PodCliqueSetControllerConfiguration) {
	if obj.ConcurrentSyncs == nil {
		obj.ConcurrentSyncs = ptr.To(1)
	}
}

// SetDefaults_PodCliqueControllerConfiguration sets defaults for the PodCliqueControllerConfiguration.
func SetDefaults_PodCliqueControllerConfiguration(obj *PodCliqueControllerConfiguration) {
	if obj.ConcurrentSyncs == nil {
		obj.ConcurrentSyncs = ptr.To(1)
	}
}

// SetDefaults_PodCliqueScalingGroupControllerConfiguration sets defaults for the PodCliqueScalignGroupControllerConfiguration.
func SetDefaults_PodCliqueScalingGroupControllerConfiguration(obj *PodCliqueScalingGroupControllerConfiguration) {
	if obj.ConcurrentSyncs == nil {
		obj.ConcurrentSyncs = ptr.To(1)
	}
}
