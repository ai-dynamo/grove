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

package constants

import "time"

const (
	// PodGangNameFileName is the name of the file that contains the PodGang name in which the pod is running.
	PodGangNameFileName = "podgangname"
	// PodNamespaceFileName is the name of the file that contains the namespace in which the pod is running.
	PodNamespaceFileName = "namespace"
	// VolumeMountPathPodInfo contains the file path at which the downward API volume is mounted.
	VolumeMountPathPodInfo = "/var/grove/pod-info"
	// OperatorNamespaceFile is the file path at which the namespace file is mounted.
	OperatorNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
	// ComponentSyncRetryInterval is a retry interval with which a reconcile request will be requeued.
	ComponentSyncRetryInterval = 5 * time.Second
	// EnvVarServiceAccountName is the name of the environment variable that stores the serviceAccountName of the operator pod.
	EnvVarServiceAccountName = "GROVE_OPERATOR_SERVICE_ACCOUNT_NAME"
)

// constants used for pod lifecycle events
const (
	// ReasonPodCreateSuccessful is an event reason which represents a successful creation of a Pod.
	ReasonPodCreateSuccessful = "PodCreateSuccessful"
	// ReasonPodCreateFailed is an event reason which represents that the creation of a Pod failed.
	ReasonPodCreateFailed = "PodCreateFailed"
	// ReasonPodDeleteSuccessful is an event reason which represents a successful deletion of a Pod.
	ReasonPodDeleteSuccessful = "PodDeleteSuccessful"
	// ReasonPodDeleteFailed is an event reason which represents that the deletion of a Pod failed.
	ReasonPodDeleteFailed = "PodDeleteFailed"
)

// constants for PodClique lifecycle events
const (
	// ReasonPodCliqueCreateSuccessful is an event reason which represents a successful creation of a PodClique.
	ReasonPodCliqueCreateSuccessful = "PodCliqueCreateSuccessful"
	// ReasonPodCliqueCreateOrUpdateSuccessful is an event reason which represents a successful creation or updation of a PodClique.
	ReasonPodCliqueCreateOrUpdateSuccessful = "PodCliqueCreateOrUpdateSuccessful"
	// ReasonPodCliqueCreateFailed is an event reason which represents that the creation of a PodClique failed.
	ReasonPodCliqueCreateFailed = "PodCliqueCreateFailed"
	// ReasonPodCliqueCreateOrUpdateFailed is an event reason which represents that the creation or updation of a PodClique failed.
	ReasonPodCliqueCreateOrUpdateFailed = "PodCliqueCreateOrUpdateFailed"
	// ReasonPodCliqueDeleteSuccessful is an event reason which represents a successful deletion of a PodClique.
	ReasonPodCliqueDeleteSuccessful = "PodCliqueDeleteSuccessful"
	// ReasonPodCliqueDeleteFailed is an event reason which represents that the deletion of a PodClique failed.
	ReasonPodCliqueDeleteFailed = "PodCliqueDeleteFailed"
)

// constants for PodCliqueScalingGroup lifecycle events
const (
	// ReasonPodCliqueScalingGroupCreateSuccessful is an event which represents a successful creation of PodCliqueScalingGroup.
	ReasonPodCliqueScalingGroupCreateSuccessful = "PodCliqueScalingGroupCreateSuccessful"
	// ReasonPodCliqueScalingGroupCreateOrUpdateFailed is an event which represents that create or update of PodCliqueScalingGroup failed.
	ReasonPodCliqueScalingGroupCreateOrUpdateFailed = "PodCliqueScalingGroupCreateOrUpdateFailed"
	// ReasonPodCliqueScalingGroupDeleteSuccessful is an event reason which represents a successful deletion of PodCliqueScalingGroup.
	ReasonPodCliqueScalingGroupDeleteSuccessful = "PodCliqueScalingGroupDeleteSuccessful"
	// ReasonPodCliqueScalingGroupDeleteFailed is an event which represents that the deletion of PodCliqueScalingGroup failed.
	ReasonPodCliqueScalingGroupDeleteFailed = "PodCliqueScalingGroupDeleteFailed"
	// ReasonPodCliqueScalingGroupReplicaDeleteSuccessful is an event reason which represents a successful deletion of a PodCliqueScalingGroup replica.
	ReasonPodCliqueScalingGroupReplicaDeleteSuccessful = "PodCliqueScalingGroupReplicaDeleteSuccessful"
	// ReasonPodCliqueScalingGroupReplicaDeleteFailed is an event which represents that the deletion of a replica of PodCliqueScalingGroup failed.
	ReasonPodCliqueScalingGroupReplicaDeleteFailed = "PodCliqueScalingGroupReplicaDeleteFailed"
)

// constants for PodGang lifecycle events
const (
	// ReasonPodGangCreateOrUpdateSuccessful is an event reason which represents a successful creation or updating of a PodGang.
	ReasonPodGangCreateOrUpdateSuccessful = "PodGangCreateOrUpdateSuccessful"
	// ReasonPodGangCreateOrUpdateFailed is an event reason which represents that the creation or updating of PodGang failed.
	ReasonPodGangCreateOrUpdateFailed = "PodGangCreateOrUpdateFailed"
	// ReasonPodGangDeleteSuccessful is an event reason which represents a successful deletion of a PodGang.
	ReasonPodGangDeleteSuccessful = "PodGangDeleteSuccessful"
	// ReasonPodGangDeleteFailed is an event reason which represents that the deletion of a PodGang failed.
	ReasonPodGangDeleteFailed = "PodGangDeleteFailed"
)

// constants for PodCliqueSet lifecycle events
const (
	// ReasonPodCliqueSetReplicaDeleteSuccessful is an event reason which represents a successful deletion of a PodCliqueSet replica.
	ReasonPodCliqueSetReplicaDeleteSuccessful = "PodCliqueSetReplicaDeleteSuccessful"
	// ReasonPodCliqueSetReplicaDeleteFailed is an event reason which represents that the deletion of a PodCliqueSet replica failed.
	ReasonPodCliqueSetReplicaDeleteFailed = "PodCliqueSetReplicaDeleteFailed"
)

// constants for ComputeDomain lifecycle events
const (
	// ReasonComputeDomainCreateSuccessful is an event reason which represents a successful creation of a ComputeDomain.
	ReasonComputeDomainCreateSuccessful = "ComputeDomainCreateSuccessful"
	// ReasonComputeDomainCreateFailed is an event reason which represents that the creation of a ComputeDomain failed.
	ReasonComputeDomainCreateFailed = "ComputeDomainCreateFailed"
	// ReasonComputeDomainDeleteSuccessful is an event reason which represents a successful deletion of a ComputeDomain.
	ReasonComputeDomainDeleteSuccessful = "ComputeDomainDeleteSuccessful"
	// ReasonComputeDomainDeleteFailed is an event reason which represents that the deletion of a ComputeDomain failed.
	ReasonComputeDomainDeleteFailed = "ComputeDomainDeleteFailed"
)
