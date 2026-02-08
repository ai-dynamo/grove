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

const (
	// OperatorName is the name of the Grove operator.
	OperatorName = "grove-operator"
	// OperatorConfigGroupName is the name of the group for Grove operator configuration.
	OperatorConfigGroupName = "operator.config.grove.io"
	// OperatorGroupName is the name of the group for all Grove custom resources.
	OperatorGroupName = "grove.io"
)

// Constants for finalizers.
const (
	// FinalizerPodCliqueSet is the finalizer for PodCliqueSet that is added to `.metadata.finalizers[]` slice. This will be placed on all PodCliqueSet resources
	// during reconciliation. This finalizer is used to clean up resources that are created for a PodCliqueSet when it is deleted.
	FinalizerPodCliqueSet = "grove.io/podcliqueset.grove.io"
	// FinalizerPodClique is the finalizer for PodClique that is added to `.metadata.finalizers[]` slice. This will be placed on all PodClique resources
	// during reconciliation. This finalizer is used to clean up resources that are created for a PodClique when it is deleted.
	FinalizerPodClique = "grove.io/podclique.grove.io"
	// FinalizerPodCliqueScalingGroup is the finalizer for PodCliqueScalingGroup that is added to `.metadata.finalizers[]` slice.
	// This will be placed on all PodCliqueScalingGroup resources during reconciliation. This finalizer is used to clean up resources
	// that are created for a PodCliqueScalingGroup when it is deleted.
	FinalizerPodCliqueScalingGroup = "grove.io/podcliquescalinggroup.grove.io"
)

const (
	// AnnotationDisableManagedResourceProtection is an annotation set by an operator on a PodCliqueSet to explicitly
	// disable protection of managed resources for a PodCliqueSet.
	AnnotationDisableManagedResourceProtection = "grove.io/disable-managed-resource-protection"
	// AnnotationTopologyName is an annotation set on PodGang to allow KAI scheduler to discover which topology to use.
	AnnotationTopologyName = "grove.io/topology-name"
)

// Constants for Grove environment variables
const (
	// EnvVarPodCliqueSetName is the environment variable name for PodCliqueSet name
	EnvVarPodCliqueSetName = "GROVE_PCS_NAME"
	// EnvVarPodCliqueSetIndex is the environment variable name for PodCliqueSet replica index
	EnvVarPodCliqueSetIndex = "GROVE_PCS_INDEX"
	// EnvVarPodCliqueName is the environment variable name for PodClique name
	EnvVarPodCliqueName = "GROVE_PCLQ_NAME"
	// EnvVarHeadlessService is the environment variable name for headless service address
	EnvVarHeadlessService = "GROVE_HEADLESS_SERVICE"
	// EnvVarPodIndex is the environment variable name for pod index within PodClique
	EnvVarPodIndex = "GROVE_PCLQ_POD_INDEX"
	// EnvVarPodCliqueScalingGroupName is the environment variable name for PodCliqueScalingGroup name
	EnvVarPodCliqueScalingGroupName = "GROVE_PCSG_NAME"
	// EnvVarPodCliqueScalingGroupIndex is the environment variable name for PodCliqueScalingGroup replica index
	EnvVarPodCliqueScalingGroupIndex = "GROVE_PCSG_INDEX"
	// EnvVarPodCliqueScalingGroupTemplateNumPods is the environment variable name for total number of pods in PodCliqueScalingGroup template
	EnvVarPodCliqueScalingGroupTemplateNumPods = "GROVE_PCSG_TEMPLATE_NUM_PODS"
)

const (
	// EventReconciling is the event type which indicates that the reconcile operation has started.
	EventReconciling = "Reconciling"
	// EventReconciled is the event type which indicates that the reconcile operation has completed successfully.
	EventReconciled = "Reconciled"
	// EventReconcileError is the event type which indicates that the reconcile operation has failed.
	EventReconcileError = "ReconcileError"
	// EventDeleting is the event type which indicates that the delete operation has started.
	EventDeleting = "Deleting"
	// EventDeleted is the event type which indicates that the delete operation has completed successfully.
	EventDeleted = "Deleted"
	// EventDeleteError is the event type which indicates that the delete operation has failed.
	EventDeleteError = "DeleteError"
)

// Constants for Condition Types
const (
	// ConditionTypeMinAvailableBreached indicates that the minimum number of ready pods in the PodClique are below the threshold defined in the PodCliqueSpec.MinAvailable threshold.
	ConditionTypeMinAvailableBreached = "MinAvailableBreached"
	// ConditionTypePodCliqueScheduled indicates that the PodClique has been successfully scheduled.
	// This condition is set to true when number of scheduled pods in the PodClique is greater than or equal to PodCliqueSpec.MinAvailable.
	ConditionTypePodCliqueScheduled = "PodCliqueScheduled"
	// ConditionTopologyLevelsUnavailable indicates that the required topology levels defined on a PodCliqueSet for topology-aware scheduling are no longer available.
	// This can happen when the ClusterTopology resource is modified which removes one or more levels required by the PodCliqueSet.
	ConditionTopologyLevelsUnavailable = "TopologyLevelsUnavailable"
)

// Constants for Condition Reasons.
const (
	// ConditionReasonInsufficientReadyPods indicates that the number of ready pods in the PodClique is below the threshold defined in the PodCliqueSpec.MinAvailable threshold.
	ConditionReasonInsufficientReadyPods = "InsufficientReadyPods"
	// ConditionReasonSufficientReadyPods indicates that the number of ready pods in the PodClique is above the threshold defined in the PodCliqueSpec.MinAvailable threshold.
	ConditionReasonSufficientReadyPods = "SufficientReadyPods"
	// ConditionReasonInsufficientScheduledPods indicates that the number of scheduled pods in the PodClique is below the threshold defined in the PodCliqueSpec.MinAvailable threshold.
	ConditionReasonInsufficientScheduledPods = "InsufficientScheduledPods"
	// ConditionReasonSufficientScheduledPods indicates that the number of scheduled pods in the PodClique greater or equal to PodCliqueSpec.MinAvailable.
	ConditionReasonSufficientScheduledPods = "SufficientScheduledPods"
	// ConditionReasonInsufficientScheduledPCSGReplicas indicates that the number of scheduled replicas in the PodCliqueScalingGroup is below the PodCliqueScalingGroupSpec.MinAvailable.
	ConditionReasonInsufficientScheduledPCSGReplicas = "InsufficientScheduledPodCliqueScalingGroupReplicas"
	// ConditionReasonInsufficientAvailablePCSGReplicas indicates that the number of ready replicas in the PodCliqueScalingGroup is below the PodCliqueScalingGroupSpec.MinAvailable.
	ConditionReasonInsufficientAvailablePCSGReplicas = "InsufficientAvailablePodCliqueScalingGroupReplicas"
	// ConditionReasonSufficientAvailablePCSGReplicas indicates that the number of ready replicas in the PodCliqueScalingGroup is greater than or equal to the PodCliqueScalingGroupSpec.MinAvailable.
	ConditionReasonSufficientAvailablePCSGReplicas = "SufficientAvailablePodCliqueScalingGroupReplicas"
	// ConditionReasonUpdateInProgress indicates that the resource is undergoing rolling update.
	ConditionReasonUpdateInProgress = "UpdateInProgress"
	// ConditionReasonNeverAvailable indicates that the PodClique has never reached its MinAvailable threshold.
	// A PodClique must have been available at least once before it can be considered in breach of MinAvailable.
	ConditionReasonNeverAvailable = "NeverAvailable"
	// ConditionReasonClusterTopologyNotFound indicates that the ClusterTopology resource required for topology-aware scheduling was not found.
	ConditionReasonClusterTopologyNotFound = "ClusterTopologyNotFound"
	// ConditionReasonTopologyLevelsUnavailable indicates that the one or more required topology levels defined on a
	// PodCliqueSet for topology-aware scheduling are no longer defined in the ClusterTopology resource.
	ConditionReasonTopologyLevelsUnavailable = "ClusterTopologyLevelsUnavailable"
	// ConditionReasonAllTopologyLevelsAvailable indicates that all required topology levels defined on a
	// PodCliqueSet for topology-aware scheduling are defined in the ClusterTopology resource.
	ConditionReasonAllTopologyLevelsAvailable = "AllClusterTopologyLevelsAvailable"
)

const (
	// KindPodCliqueSet is the kind for a PodCliqueSet resource.
	KindPodCliqueSet = "PodCliqueSet"
	// KindPodClique is the kind for a PodClique resource.
	KindPodClique = "PodClique"
	// KindPodCliqueScalingGroup is the kind for a PodCliqueScalingGroup resource.
	KindPodCliqueScalingGroup = "PodCliqueScalingGroup"
	// KindClusterTopology is the kind for a ClusterTopology resource.
	KindClusterTopology = "ClusterTopology"
)
