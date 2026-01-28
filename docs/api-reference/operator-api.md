# API Reference

## Packages
- [grove.io/v1alpha1](#groveiov1alpha1)
- [operator.config.grove.io/v1alpha1](#operatorconfiggroveiov1alpha1)


## grove.io/v1alpha1


### Resource Types
- [ClusterTopology](#clustertopology)
- [PodClique](#podclique)
- [PodCliqueScalingGroup](#podcliquescalinggroup)
- [PodCliqueSet](#podcliqueset)



#### AutoScalingConfig



AutoScalingConfig defines the configuration for the horizontal pod autoscaler.



_Appears in:_
- [PodCliqueScalingGroupConfig](#podcliquescalinggroupconfig)
- [PodCliqueSpec](#podcliquespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `minReplicas` _integer_ | MinReplicas is the lower limit for the number of replicas for the target resource.<br />It will be used by the horizontal pod autoscaler to determine the minimum number of replicas to scale-in to. |  |  |
| `maxReplicas` _integer_ | maxReplicas is the upper limit for the number of replicas to which the autoscaler can scale up.<br />It cannot be less that minReplicas. |  |  |
| `metrics` _[MetricSpec](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#metricspec-v2-autoscaling) array_ | Metrics contains the specifications for which to use to calculate the<br />desired replica count (the maximum replica count across all metrics will<br />be used).  The desired replica count is calculated multiplying the<br />ratio between the target value and the current value by the current<br />number of pods.  Ergo, metrics used must decrease as the pod count is<br />increased, and vice versa.  See the individual metric source types for<br />more information about how each type of metric must respond.<br />If not set, the default metric will be set to 80% average CPU utilization. |  |  |


#### CliqueStartupType

_Underlying type:_ _string_

CliqueStartupType defines the order in which each PodClique is started.

_Validation:_
- Enum: [CliqueStartupTypeAnyOrder CliqueStartupTypeInOrder CliqueStartupTypeExplicit]

_Appears in:_
- [PodCliqueSetTemplateSpec](#podcliquesettemplatespec)

| Field | Description |
| --- | --- |
| `CliqueStartupTypeAnyOrder` | CliqueStartupTypeAnyOrder defines that the cliques can be started in any order. This allows for concurrent starts of cliques.<br />This is the default CliqueStartupType.<br /> |
| `CliqueStartupTypeInOrder` | CliqueStartupTypeInOrder defines that the cliques should be started in the order they are defined in the PodGang Cliques slice.<br /> |
| `CliqueStartupTypeExplicit` | CliqueStartupTypeExplicit defines that the cliques should be started after the cliques defined in PodClique.StartsAfter have started.<br /> |


#### ClusterTopology



ClusterTopology defines the topology hierarchy for the cluster.
This resource is immutable after creation.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `grove.io/v1alpha1` | | |
| `kind` _string_ | `ClusterTopology` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[ClusterTopologySpec](#clustertopologyspec)_ | Spec defines the topology hierarchy specification. |  |  |


#### ClusterTopologySpec



ClusterTopologySpec defines the topology hierarchy specification.



_Appears in:_
- [ClusterTopology](#clustertopology)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `levels` _[TopologyLevel](#topologylevel) array_ | Levels is an ordered list of topology levels from broadest to narrowest scope.<br />The order in this list defines the hierarchy (index 0 = broadest level).<br />This field is immutable after creation. |  | MaxItems: 7 <br />MinItems: 1 <br /> |


#### ErrorCode

_Underlying type:_ _string_

ErrorCode is a custom error code that uniquely identifies an error.



_Appears in:_
- [LastError](#lasterror)



#### HeadlessServiceConfig



HeadlessServiceConfig defines the config options for the headless service.



_Appears in:_
- [PodCliqueSetTemplateSpec](#podcliquesettemplatespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `publishNotReadyAddresses` _boolean_ | PublishNotReadyAddresses if set to true will publish the DNS records of pods even if the pods are not ready.<br /> if not set, it defaults to true. | true |  |


#### LastError



LastError captures the last error observed by the controller when reconciling an object.



_Appears in:_
- [PodCliqueScalingGroupStatus](#podcliquescalinggroupstatus)
- [PodCliqueSetStatus](#podcliquesetstatus)
- [PodCliqueStatus](#podcliquestatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `code` _[ErrorCode](#errorcode)_ | Code is the error code that uniquely identifies the error. |  |  |
| `description` _string_ | Description is a human-readable description of the error. |  |  |
| `observedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#time-v1-meta)_ | ObservedAt is the time at which the error was observed. |  |  |




#### LastOperationState

_Underlying type:_ _string_

LastOperationState is a string alias for the state of the last operation.



_Appears in:_
- [LastOperation](#lastoperation)

| Field | Description |
| --- | --- |
| `Processing` | LastOperationStateProcessing indicates that the last operation is in progress.<br /> |
| `Succeeded` | LastOperationStateSucceeded indicates that the last operation succeeded.<br /> |
| `Error` | LastOperationStateError indicates that the last operation completed with errors and will be retried.<br /> |


#### LastOperationType

_Underlying type:_ _string_

LastOperationType is a string alias for the type of the last operation.



_Appears in:_
- [LastOperation](#lastoperation)

| Field | Description |
| --- | --- |
| `Reconcile` | LastOperationTypeReconcile indicates that the last operation was a reconcile operation.<br /> |
| `Delete` | LastOperationTypeDelete indicates that the last operation was a delete operation.<br /> |


#### PodClique



PodClique is a set of pods running the same image.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `grove.io/v1alpha1` | | |
| `kind` _string_ | `PodClique` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[PodCliqueSpec](#podcliquespec)_ | Spec defines the specification of a PodClique. |  |  |
| `status` _[PodCliqueStatus](#podcliquestatus)_ | Status defines the status of a PodClique. |  |  |


#### PodCliqueRollingUpdateProgress



PodCliqueRollingUpdateProgress provides details about the ongoing rolling update of the PodClique.



_Appears in:_
- [PodCliqueStatus](#podcliquestatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `updateStartedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#time-v1-meta)_ | UpdateStartedAt is the time at which the rolling update started. |  |  |
| `updateEndedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#time-v1-meta)_ | UpdateEndedAt is the time at which the rolling update ended.<br />It will be set to nil if the rolling update is still in progress. |  |  |
| `podCliqueSetGenerationHash` _string_ | PodCliqueSetGenerationHash is the PodCliqueSet generation hash corresponding to the PodCliqueSet spec that is being rolled out.<br />While the update is in progress PodCliqueStatus.CurrentPodCliqueSetGenerationHash will not match this hash. Once the update is complete the<br />value of this field will be copied to PodCliqueStatus.CurrentPodCliqueSetGenerationHash. |  |  |
| `podTemplateHash` _string_ | PodTemplateHash is the PodClique template hash corresponding to the PodClique spec that is being rolled out.<br />While the update is in progress PodCliqueStatus.CurrentPodTemplateHash will not match this hash. Once the update is complete the<br />value of this field will be copied to PodCliqueStatus.CurrentPodTemplateHash. |  |  |
| `readyPodsSelectedToUpdate` _[PodsSelectedToUpdate](#podsselectedtoupdate)_ | ReadyPodsSelectedToUpdate captures the pod names of ready Pods that are either currently being updated or have been previously updated. |  |  |


#### PodCliqueScalingGroup



PodCliqueScalingGroup is the schema to define scaling groups that is used to scale a group of PodClique's.
An instance of this custom resource will be created for every pod clique scaling group defined as part of PodCliqueSet.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `grove.io/v1alpha1` | | |
| `kind` _string_ | `PodCliqueScalingGroup` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[PodCliqueScalingGroupSpec](#podcliquescalinggroupspec)_ | Spec is the specification of the PodCliqueScalingGroup. |  |  |
| `status` _[PodCliqueScalingGroupStatus](#podcliquescalinggroupstatus)_ | Status is the status of the PodCliqueScalingGroup. |  |  |


#### PodCliqueScalingGroupConfig



PodCliqueScalingGroupConfig is a group of PodClique's that are scaled together.
Each member PodClique.Replicas will be computed as a product of PodCliqueScalingGroupConfig.Replicas and PodCliqueTemplateSpec.Spec.Replicas.
NOTE: If a PodCliqueScalingGroupConfig is defined, then for the member PodClique's, individual AutoScalingConfig cannot be defined.



_Appears in:_
- [PodCliqueSetTemplateSpec](#podcliquesettemplatespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the PodCliqueScalingGroupConfig. This should be unique within the PodCliqueSet.<br />It allows consumers to give a semantic name to a group of PodCliques that needs to be scaled together. |  |  |
| `cliqueNames` _string array_ | CliqueNames is the list of names of the PodClique's that are part of the scaling group. |  |  |
| `replicas` _integer_ | Replicas is the desired number of replicas for the scaling group at template level.<br />This allows one to control the replicas of the scaling group at startup.<br />If not specified, it defaults to 1. | 1 |  |
| `minAvailable` _integer_ | MinAvailable serves two purposes:<br />Gang Scheduling:<br />It defines the minimum number of replicas that are guaranteed to be gang scheduled.<br />Gang Termination:<br />It defines the minimum requirement of available replicas for a PodCliqueScalingGroup.<br />Violation of this threshold for a duration beyond TerminationDelay will result in termination of the PodCliqueSet replica that it belongs to.<br />Default: If not specified, it defaults to 1.<br />Constraints:<br />MinAvailable cannot be greater than Replicas.<br />If ScaleConfig is defined then its MinAvailable should not be less than ScaleConfig.MinReplicas. | 1 |  |
| `scaleConfig` _[AutoScalingConfig](#autoscalingconfig)_ | ScaleConfig is the horizontal pod autoscaler configuration for the pod clique scaling group. |  |  |
| `topologyConstraint` _[TopologyConstraint](#topologyconstraint)_ | TopologyConstraint defines topology placement requirements for PodCliqueScalingGroup.<br />Must be equal to or stricter than parent PodCliqueSet constraints. |  |  |


#### PodCliqueScalingGroupReplicaRollingUpdateProgress



PodCliqueScalingGroupReplicaRollingUpdateProgress provides details about the rolling update progress of ready replicas of PodCliqueScalingGroup that have been selected for update.



_Appears in:_
- [PodCliqueScalingGroupRollingUpdateProgress](#podcliquescalinggrouprollingupdateprogress)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `current` _integer_ | Current is the index of the PodCliqueScalingGroup replica that is currently being updated. |  |  |
| `completed` _integer array_ | Completed is the list of indices of PodCliqueScalingGroup replicas that have been updated to the latest PodCliqueSet spec. |  |  |


#### PodCliqueScalingGroupRollingUpdateProgress



PodCliqueScalingGroupRollingUpdateProgress provides details about the ongoing rolling update of the PodCliqueScalingGroup.



_Appears in:_
- [PodCliqueScalingGroupStatus](#podcliquescalinggroupstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `updateStartedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#time-v1-meta)_ | UpdateStartedAt is the time at which the rolling update started. |  |  |
| `updateEndedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#time-v1-meta)_ | UpdateEndedAt is the time at which the rolling update ended. |  |  |
| `podCliqueSetGenerationHash` _string_ | PodCliqueSetGenerationHash is the PodCliqueSet generation hash corresponding to the PodCliqueSet spec that is being rolled out.<br />While the update is in progress PodCliqueScalingGroupStatus.CurrentPodCliqueSetGenerationHash will not match this hash. Once the update is complete the<br />value of this field will be copied to PodCliqueScalingGroupStatus.CurrentPodCliqueSetGenerationHash. |  |  |
| `updatedPodCliques` _string array_ | UpdatedPodCliques is the list of PodClique names that have been updated to the latest PodCliqueSet spec. |  |  |
| `readyReplicaIndicesSelectedToUpdate` _[PodCliqueScalingGroupReplicaRollingUpdateProgress](#podcliquescalinggroupreplicarollingupdateprogress)_ | ReadyReplicaIndicesSelectedToUpdate provides the rolling update progress of ready replicas of PodCliqueScalingGroup that have been selected for update.<br />PodCliqueScalingGroup replicas that are either pending or unhealthy will be force updated and the update will not wait for these replicas to become ready.<br />For all ready replicas, one replica is chosen at a time to update, once it is updated and becomes ready, the next ready replica is chosen for update. |  |  |


#### PodCliqueScalingGroupSpec



PodCliqueScalingGroupSpec is the specification of the PodCliqueScalingGroup.



_Appears in:_
- [PodCliqueScalingGroup](#podcliquescalinggroup)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `replicas` _integer_ | Replicas is the desired number of replicas for the PodCliqueScalingGroup.<br />If not specified, it defaults to 1. | 1 |  |
| `minAvailable` _integer_ | MinAvailable specifies the minimum number of ready replicas required for a PodCliqueScalingGroup to be considered operational.<br />A PodCliqueScalingGroup replica is considered "ready" when its associated PodCliques have sufficient ready or starting pods.<br />If MinAvailable is breached, it will be used to signal that the PodCliqueScalingGroup is no longer operating with the desired availability.<br />MinAvailable cannot be greater than Replicas. If ScaleConfig is defined then its MinAvailable should not be less than ScaleConfig.MinReplicas.<br />It serves two main purposes:<br />1. Gang Scheduling: MinAvailable defines the minimum number of replicas that are guaranteed to be gang scheduled.<br />2. Gang Termination: MinAvailable is used as a lower bound below which a PodGang becomes a candidate for Gang termination.<br />If not specified, it defaults to 1. | 1 |  |
| `cliqueNames` _string array_ | CliqueNames is the list of PodClique names that are configured in the<br />matching PodCliqueScalingGroup in PodCliqueSet.Spec.Template.PodCliqueScalingGroupConfigs. |  |  |


#### PodCliqueScalingGroupStatus



PodCliqueScalingGroupStatus is the status of the PodCliqueScalingGroup.



_Appears in:_
- [PodCliqueScalingGroup](#podcliquescalinggroup)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `replicas` _integer_ | Replicas is the observed number of replicas for the PodCliqueScalingGroup. |  |  |
| `scheduledReplicas` _integer_ | ScheduledReplicas is the number of replicas that are scheduled for the PodCliqueScalingGroup.<br />A replica of PodCliqueScalingGroup is considered "scheduled" when at least MinAvailable number<br />of pods in each constituent PodClique has been scheduled. | 0 |  |
| `availableReplicas` _integer_ | AvailableReplicas is the number of PodCliqueScalingGroup replicas that are available.<br />A PodCliqueScalingGroup replica is considered available when all constituent PodClique's have<br />PodClique.Status.ReadyReplicas greater than or equal to PodClique.Spec.MinAvailable | 0 |  |
| `updatedReplicas` _integer_ | UpdatedReplicas is the number of PodCliqueScalingGroup replicas that correspond with the latest PodCliqueSetGenerationHash. | 0 |  |
| `selector` _string_ | Selector is the selector used to identify the pods that belong to this scaling group. |  |  |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `lastErrors` _[LastError](#lasterror) array_ | LastErrors captures the last errors observed by the controller when reconciling the PodClique. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#condition-v1-meta) array_ | Conditions represents the latest available observations of the PodCliqueScalingGroup by its controller. |  |  |
| `currentPodCliqueSetGenerationHash` _string_ | CurrentPodCliqueSetGenerationHash establishes a correlation to PodCliqueSet generation hash indicating<br />that the spec of the PodCliqueSet at this generation is fully realized in the PodCliqueScalingGroup. |  |  |
| `rollingUpdateProgress` _[PodCliqueScalingGroupRollingUpdateProgress](#podcliquescalinggrouprollingupdateprogress)_ | RollingUpdateProgress provides details about the ongoing rolling update of the PodCliqueScalingGroup. |  |  |


#### PodCliqueSet



PodCliqueSet is a set of PodGangs defining specification on how to spread and manage a gang of pods and monitoring their status.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `grove.io/v1alpha1` | | |
| `kind` _string_ | `PodCliqueSet` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[PodCliqueSetSpec](#podcliquesetspec)_ | Spec defines the specification of the PodCliqueSet. |  |  |
| `status` _[PodCliqueSetStatus](#podcliquesetstatus)_ | Status defines the status of the PodCliqueSet. |  |  |


#### PodCliqueSetReplicaRollingUpdateProgress



PodCliqueSetReplicaRollingUpdateProgress captures the progress of a rolling update for a specific PodCliqueSet replica.



_Appears in:_
- [PodCliqueSetRollingUpdateProgress](#podcliquesetrollingupdateprogress)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `replicaIndex` _integer_ | ReplicaIndex is the replica index of the PodCliqueSet that is being updated. |  |  |
| `updateStartedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#time-v1-meta)_ | UpdateStartedAt is the time at which the rolling update started for this PodCliqueSet replica index. |  |  |


#### PodCliqueSetRollingUpdateProgress



PodCliqueSetRollingUpdateProgress captures the progress of a rolling update of the PodCliqueSet.



_Appears in:_
- [PodCliqueSetStatus](#podcliquesetstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `updateStartedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#time-v1-meta)_ | UpdateStartedAt is the time at which the rolling update started for the PodCliqueSet. |  |  |
| `updateEndedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#time-v1-meta)_ | UpdateEndedAt is the time at which the rolling update ended for the PodCliqueSet. |  |  |
| `updatedPodCliqueScalingGroups` _string array_ | UpdatedPodCliqueScalingGroups is a list of PodCliqueScalingGroup names that have been updated to the desired PodCliqueSet generation hash. |  |  |
| `updatedPodCliques` _string array_ | UpdatedPodCliques is a list of PodClique names that have been updated to the desired PodCliqueSet generation hash. |  |  |
| `currentlyUpdating` _[PodCliqueSetReplicaRollingUpdateProgress](#podcliquesetreplicarollingupdateprogress)_ | CurrentlyUpdating captures the progress of the PodCliqueSet replica that is currently being updated. |  |  |


#### PodCliqueSetSpec



PodCliqueSetSpec defines the specification of a PodCliqueSet.



_Appears in:_
- [PodCliqueSet](#podcliqueset)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `replicas` _integer_ | Replicas is the number of desired replicas of the PodGang. | 0 |  |
| `template` _[PodCliqueSetTemplateSpec](#podcliquesettemplatespec)_ | Template describes the template spec for PodGangs that will be created in the PodCliqueSet. |  |  |


#### PodCliqueSetStatus



PodCliqueSetStatus defines the status of a PodCliqueSet.



_Appears in:_
- [PodCliqueSet](#podcliqueset)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#condition-v1-meta) array_ | Conditions represents the latest available observations of the PodCliqueSet by its controller. |  |  |
| `lastErrors` _[LastError](#lasterror) array_ | LastErrors captures the last errors observed by the controller when reconciling the PodCliqueSet. |  |  |
| `replicas` _integer_ | Replicas is the total number of PodCliqueSet replicas created. |  |  |
| `updatedReplicas` _integer_ | UpdatedReplicas is the number of replicas that have been updated to the desired revision of the PodCliqueSet. | 0 |  |
| `availableReplicas` _integer_ | AvailableReplicas is the number of PodCliqueSet replicas that are available.<br />A PodCliqueSet replica is considered available when all standalone PodCliques within that replica<br />have MinAvailableBreached condition = False AND all PodCliqueScalingGroups (PCSG) within that replica<br />have MinAvailableBreached condition = False. | 0 |  |
| `hpaPodSelector` _string_ | Selector is the label selector that determines which pods are part of the PodGang.<br />PodGang is a unit of scale and this selector is used by HPA to scale the PodGang based on metrics captured for the pods that match this selector. |  |  |
| `podGangStatuses` _[PodGangStatus](#podgangstatus) array_ | PodGangStatuses captures the status for all the PodGang's that are part of the PodCliqueSet. |  |  |
| `currentGenerationHash` _string_ | CurrentGenerationHash is a hash value generated out of a collection of fields in a PodCliqueSet.<br />Since only a subset of fields is taken into account when generating the hash, not every change in the PodCliqueSetSpec will<br />be accounted for when generating this hash value. A field in PodCliqueSetSpec is included if a change to it triggers<br />a rolling update of PodCliques and/or PodCliqueScalingGroups.<br />Only if this value is not nil and the newly computed hash value is different from the persisted CurrentGenerationHash value<br />then a rolling update needs to be triggerred. |  |  |
| `rollingUpdateProgress` _[PodCliqueSetRollingUpdateProgress](#podcliquesetrollingupdateprogress)_ | RollingUpdateProgress represents the progress of a rolling update. |  |  |


#### PodCliqueSetTemplateSpec



PodCliqueSetTemplateSpec defines a template spec for a PodGang.
A PodGang does not have a RestartPolicy field because the restart policy is predefined:
If the number of pods in any of the cliques falls below the threshold, the entire PodGang will be restarted.
The threshold is determined by either:
- The value of "MinReplicas", if specified in the ScaleConfig of that clique, or
- The "Replicas" value of that clique



_Appears in:_
- [PodCliqueSetSpec](#podcliquesetspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `cliques` _[PodCliqueTemplateSpec](#podcliquetemplatespec) array_ | Cliques is a slice of cliques that make up the PodGang. There should be at least one PodClique. |  |  |
| `cliqueStartupType` _[CliqueStartupType](#cliquestartuptype)_ | StartupType defines the type of startup dependency amongst the cliques within a PodGang.<br />If it is not defined then default of CliqueStartupTypeAnyOrder is used. | CliqueStartupTypeAnyOrder | Enum: [CliqueStartupTypeAnyOrder CliqueStartupTypeInOrder CliqueStartupTypeExplicit] <br /> |
| `priorityClassName` _string_ | PriorityClassName is the name of the PriorityClass to be used for the PodCliqueSet.<br />If specified, indicates the priority of the PodCliqueSet. "system-node-critical" and<br />"system-cluster-critical" are two special keywords which indicate the<br />highest priorities with the former being the highest priority. Any other<br />name must be defined by creating a PriorityClass object with that name.<br />If not specified, the pod priority will be default or zero if there is no default. |  |  |
| `headlessServiceConfig` _[HeadlessServiceConfig](#headlessserviceconfig)_ | HeadlessServiceConfig defines the config options for the headless service.<br />If present, create headless service for each PodGang. |  |  |
| `topologyConstraint` _[TopologyConstraint](#topologyconstraint)_ | TopologyConstraint defines topology placement requirements for PodCliqueSet. |  |  |
| `terminationDelay` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#duration-v1-meta)_ | TerminationDelay is the delay after which the gang termination will be triggered.<br />A gang is a candidate for termination if number of running pods fall below a threshold for any PodClique.<br />If a PodGang remains a candidate past TerminationDelay then it will be terminated. This allows additional time<br />to the kube-scheduler to re-schedule sufficient pods in the PodGang that will result in having the total number of<br />running pods go above the threshold.<br />Defaults to 4 hours. |  |  |
| `podCliqueScalingGroups` _[PodCliqueScalingGroupConfig](#podcliquescalinggroupconfig) array_ | PodCliqueScalingGroupConfigs is a list of scaling groups for the PodCliqueSet. |  |  |


#### PodCliqueSpec



PodCliqueSpec defines the specification of a PodClique.



_Appears in:_
- [PodClique](#podclique)
- [PodCliqueTemplateSpec](#podcliquetemplatespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `roleName` _string_ | RoleName is the name of the role that this PodClique will assume. |  |  |
| `podSpec` _[PodSpec](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#podspec-v1-core)_ | Spec is the spec of the pods in the clique. |  |  |
| `replicas` _integer_ | Replicas is the number of replicas of the pods in the clique. It cannot be less than 1. |  |  |
| `minAvailable` _integer_ | MinAvailable serves two purposes:<br />1. It defines the minimum number of pods that are guaranteed to be gang scheduled.<br />2. It defines the minimum requirement of available pods in a PodClique. Violation of this threshold will result in termination of the PodGang that it belongs to.<br />If MinAvailable is not set, then it will default to the template Replicas. |  |  |
| `startsAfter` _string array_ | StartsAfter provides you a way to explicitly define the startup dependencies amongst cliques.<br />If CliqueStartupType in PodGang has been set to 'CliqueStartupTypeExplicit', then to create an ordered start amongst PodClique's StartsAfter can be used.<br />A forest of DAG's can be defined to model any start order dependencies. If there are more than one PodClique's defined and StartsAfter is not set for any of them,<br />then their startup order is random at best and must not be relied upon.<br />Validations:<br />1. If a StartsAfter has been defined and one or more cycles are detected in DAG's then it will be flagged as validation error.<br />2. If StartsAfter is defined and does not identify any PodClique then it will be flagged as a validation error. |  |  |
| `autoScalingConfig` _[AutoScalingConfig](#autoscalingconfig)_ | ScaleConfig is the horizontal pod autoscaler configuration for a PodClique. |  |  |


#### PodCliqueStatus



PodCliqueStatus defines the status of a PodClique.



_Appears in:_
- [PodClique](#podclique)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `lastErrors` _[LastError](#lasterror) array_ | LastErrors captures the last errors observed by the controller when reconciling the PodClique. |  |  |
| `replicas` _integer_ | Replicas is the total number of non-terminated Pods targeted by this PodClique. |  |  |
| `readyReplicas` _integer_ | ReadyReplicas is the number of ready Pods targeted by this PodClique. | 0 |  |
| `updatedReplicas` _integer_ | UpdatedReplicas is the number of Pods that have been updated and are at the desired revision of the PodClique. | 0 |  |
| `scheduleGatedReplicas` _integer_ | ScheduleGatedReplicas is the number of Pods that have been created with one or more scheduling gate(s) set.<br />Sum of ReadyReplicas and ScheduleGatedReplicas will always be <= Replicas. | 0 |  |
| `scheduledReplicas` _integer_ | ScheduledReplicas is the number of Pods that have been scheduled by the kube-scheduler. | 0 |  |
| `hpaPodSelector` _string_ | Selector is the label selector that determines which pods are part of the PodClique.<br />PodClique is a unit of scale and this selector is used by HPA to scale the PodClique based on metrics captured for the pods that match this selector. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#condition-v1-meta) array_ | Conditions represents the latest available observations of the clique by its controller. |  |  |
| `currentPodCliqueSetGenerationHash` _string_ | CurrentPodCliqueSetGenerationHash establishes a correlation to PodCliqueSet generation hash indicating<br />that the spec of the PodCliqueSet at this generation is fully realized in the PodClique. |  |  |
| `currentPodTemplateHash` _string_ | CurrentPodTemplateHash establishes a correlation to PodClique template hash indicating<br />that the spec of the PodClique at this template hash is fully realized in the PodClique. |  |  |
| `rollingUpdateProgress` _[PodCliqueRollingUpdateProgress](#podcliquerollingupdateprogress)_ | RollingUpdateProgress provides details about the ongoing rolling update of the PodClique. |  |  |


#### PodCliqueTemplateSpec



PodCliqueTemplateSpec defines a template spec for a PodClique.



_Appears in:_
- [PodCliqueSetTemplateSpec](#podcliquesettemplatespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name must be unique within a PodCliqueSet and is used to denote a role.<br />Once set it cannot be updated.<br />More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/names#names |  |  |
| `labels` _object (keys:string, values:string)_ | Labels is a map of string keys and values that can be used to organize and categorize<br />(scope and select) objects. May match selectors of replication controllers<br />and services.<br />More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/labels |  |  |
| `annotations` _object (keys:string, values:string)_ | Annotations is an unstructured key value map stored with a resource that may be<br />set by external tools to store and retrieve arbitrary metadata. They are not<br />queryable and should be preserved when modifying objects.<br />More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/annotations |  |  |
| `topologyConstraint` _[TopologyConstraint](#topologyconstraint)_ | TopologyConstraint defines topology placement requirements for PodClique.<br />Must be equal to or stricter than parent resource constraints. |  |  |
| `spec` _[PodCliqueSpec](#podcliquespec)_ | Specification of the desired behavior of a PodClique.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#spec-and-status |  |  |


#### PodGangPhase

_Underlying type:_ _string_

PodGangPhase represents the phase of a PodGang.

_Validation:_
- Enum: [Pending Starting Running Failed Succeeded]

_Appears in:_
- [PodGangStatus](#podgangstatus)

| Field | Description |
| --- | --- |
| `Pending` | PodGangPending indicates that the pods in a PodGang have not yet been taken up for scheduling.<br /> |
| `Starting` | PodGangStarting indicates that the pods are bound to nodes by the scheduler and are starting.<br /> |
| `Running` | PodGangRunning indicates that the all the pods in a PodGang are running.<br /> |
| `Failed` | PodGangFailed indicates that one or more pods in a PodGang have failed.<br />This is a terminal state and is typically used for batch jobs.<br /> |
| `Succeeded` | PodGangSucceeded indicates that all the pods in a PodGang have succeeded.<br />This is a terminal state and is typically used for batch jobs.<br /> |


#### PodGangStatus



PodGangStatus defines the status of a PodGang.



_Appears in:_
- [PodCliqueSetStatus](#podcliquesetstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the PodGang. |  |  |
| `phase` _[PodGangPhase](#podgangphase)_ | Phase is the current phase of the PodGang. |  | Enum: [Pending Starting Running Failed Succeeded] <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#condition-v1-meta) array_ | Conditions represents the latest available observations of the PodGang by its controller. |  |  |


#### PodsSelectedToUpdate



PodsSelectedToUpdate captures the current and previous set of pod names that have been selected for update in a rolling update.



_Appears in:_
- [PodCliqueRollingUpdateProgress](#podcliquerollingupdateprogress)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `current` _string_ | Current captures the current pod name that is a target for update. |  |  |
| `completed` _string array_ | Completed captures the pod names that have already been updated. |  |  |


#### TopologyConstraint



TopologyConstraint defines topology placement requirements.



_Appears in:_
- [PodCliqueScalingGroupConfig](#podcliquescalinggroupconfig)
- [PodCliqueSetTemplateSpec](#podcliquesettemplatespec)
- [PodCliqueTemplateSpec](#podcliquetemplatespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `packDomain` _[TopologyDomain](#topologydomain)_ | PackDomain specifies the topology domain for grouping replicas.<br />Controls placement constraint for EACH individual replica instance.<br />Must be one of: region, zone, datacenter, block, rack, host, numa<br />Example: "rack" means each replica independently placed within one rack.<br />Note: Does NOT constrain all replicas to the same rack together.<br />Different replicas can be in different topology domains. |  | Enum: [region zone datacenter block rack host numa] <br /> |


#### TopologyDomain

_Underlying type:_ _string_

TopologyDomain represents a level in the cluster topology hierarchy.



_Appears in:_
- [TopologyConstraint](#topologyconstraint)
- [TopologyLevel](#topologylevel)

| Field | Description |
| --- | --- |
| `region` | TopologyDomainRegion represents the region level in the topology hierarchy.<br /> |
| `zone` | TopologyDomainZone represents the zone level in the topology hierarchy.<br /> |
| `datacenter` | TopologyDomainDataCenter represents the datacenter level in the topology hierarchy.<br /> |
| `block` | TopologyDomainBlock represents the block level in the topology hierarchy.<br /> |
| `rack` | TopologyDomainRack represents the rack level in the topology hierarchy.<br /> |
| `host` | TopologyDomainHost represents the host level in the topology hierarchy.<br /> |
| `numa` | TopologyDomainNuma represents the numa level in the topology hierarchy.<br /> |


#### TopologyLevel



TopologyLevel defines a single level in the topology hierarchy.
Maps a platform-agnostic domain to a platform-specific node label key,
allowing workload operators a consistent way to reference topology levels when defining TopologyConstraint's.



_Appears in:_
- [ClusterTopologySpec](#clustertopologyspec)
- [TopologyAwareSchedulingConfiguration](#topologyawareschedulingconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `domain` _[TopologyDomain](#topologydomain)_ | Domain is a platform provider-agnostic level identifier.<br />Must be one of: region, zone, datacenter, block, rack, host, numa |  | Enum: [region zone datacenter block rack host numa] <br />Required: \{\} <br /> |
| `key` _string_ | Key is the node label key that identifies this topology domain.<br />Must be a valid Kubernetes label key (qualified name).<br />Examples: "topology.kubernetes.io/zone", "kubernetes.io/hostname" |  | MaxLength: 63 <br />MinLength: 1 <br />Pattern: `^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]/)?([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]$` <br />Required: \{\} <br /> |



## operator.config.grove.io/v1alpha1




#### AuthorizerConfig



AuthorizerConfig defines the configuration for the authorizer admission webhook.



_Appears in:_
- [OperatorConfiguration](#operatorconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled indicates whether the authorizer is enabled. |  |  |
| `exemptServiceAccountUserNames` _string array_ | ExemptServiceAccountUserNames is a list of service account usernames that are exempt from authorizer checks.<br />Each service account username name in ExemptServiceAccountUserNames should be of the following format:<br />system:serviceaccount:<namespace>:<service-account-name>. ServiceAccounts are represented in this<br />format when checking the username in authenticationv1.UserInfo.Name. |  |  |


#### CertProvisionMode

_Underlying type:_ _string_

CertProvisionMode defines how webhook certificates are provisioned.

_Validation:_
- Enum: [auto manual]

_Appears in:_
- [WebhookServer](#webhookserver)

| Field | Description |
| --- | --- |
| `auto` | CertProvisionModeAuto enables automatic certificate generation and management via cert-controller.<br />cert-controller automatically generates self-signed certificates and stores them in the Secret.<br /> |
| `manual` | CertProvisionModeManual expects certificates to be provided externally (e.g., by cert-manager, cluster admin).<br /> |


#### ClientConnectionConfiguration



ClientConnectionConfiguration defines the configuration for constructing a client.



_Appears in:_
- [OperatorConfiguration](#operatorconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `qps` _float_ | QPS controls the number of queries per second allowed for this connection. |  |  |
| `burst` _integer_ | Burst allows extra queries to accumulate when a client is exceeding its rate. |  |  |
| `contentType` _string_ | ContentType is the content type used when sending data to the server from this client. |  |  |
| `acceptContentTypes` _string_ | AcceptContentTypes defines the Accept header sent by clients when connecting to the server,<br />overriding the default value of 'application/json'. This field will control all connections<br />to the server used by a particular client. |  |  |


#### ControllerConfiguration



ControllerConfiguration defines the configuration for the controllers.



_Appears in:_
- [OperatorConfiguration](#operatorconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `podCliqueSet` _[PodCliqueSetControllerConfiguration](#podcliquesetcontrollerconfiguration)_ | PodCliqueSet is the configuration for the PodCliqueSet controller. |  |  |
| `podClique` _[PodCliqueControllerConfiguration](#podcliquecontrollerconfiguration)_ | PodClique is the configuration for the PodClique controller. |  |  |
| `podCliqueScalingGroup` _[PodCliqueScalingGroupControllerConfiguration](#podcliquescalinggroupcontrollerconfiguration)_ | PodCliqueScalingGroup is the configuration for the PodCliqueScalingGroup controller. |  |  |


#### DebuggingConfiguration



DebuggingConfiguration defines the configuration for debugging.



_Appears in:_
- [OperatorConfiguration](#operatorconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enableProfiling` _boolean_ | EnableProfiling enables profiling via host:port/debug/pprof/ endpoints. |  |  |


#### LeaderElectionConfiguration



LeaderElectionConfiguration defines the configuration for the leader election.



_Appears in:_
- [OperatorConfiguration](#operatorconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled specifies whether leader election is enabled. Set this<br />to true when running replicated instances of the operator for high availability. |  |  |
| `leaseDuration` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#duration-v1-meta)_ | LeaseDuration is the duration that non-leader candidates will wait<br />after observing a leadership renewal until attempting to acquire<br />leadership of the occupied but un-renewed leader slot. This is effectively the<br />maximum duration that a leader can be stopped before it is replaced<br />by another candidate. This is only applicable if leader election is<br />enabled. |  |  |
| `renewDeadline` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#duration-v1-meta)_ | RenewDeadline is the interval between attempts by the acting leader to<br />renew its leadership before it stops leading. This must be less than or<br />equal to the lease duration.<br />This is only applicable if leader election is enabled. |  |  |
| `retryPeriod` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#duration-v1-meta)_ | RetryPeriod is the duration leader elector clients should wait<br />between attempting acquisition and renewal of leadership.<br />This is only applicable if leader election is enabled. |  |  |
| `resourceLock` _string_ | ResourceLock determines which resource lock to use for leader election.<br />This is only applicable if leader election is enabled. |  |  |
| `resourceName` _string_ | ResourceName determines the name of the resource that leader election<br />will use for holding the leader lock.<br />This is only applicable if leader election is enabled. |  |  |
| `resourceNamespace` _string_ | ResourceNamespace determines the namespace in which the leader<br />election resource will be created.<br />This is only applicable if leader election is enabled. |  |  |


#### LogFormat

_Underlying type:_ _string_

LogFormat defines the format of the log.



_Appears in:_
- [OperatorConfiguration](#operatorconfiguration)

| Field | Description |
| --- | --- |
| `json` | LogFormatJSON is the JSON log format.<br /> |
| `text` | LogFormatText is the text log format.<br /> |


#### LogLevel

_Underlying type:_ _string_

LogLevel defines the log level.



_Appears in:_
- [OperatorConfiguration](#operatorconfiguration)

| Field | Description |
| --- | --- |
| `debug` | DebugLevel is the debug log level, i.e. the most verbose.<br /> |
| `info` | InfoLevel is the default log level.<br /> |
| `error` | ErrorLevel is a log level where only errors are logged.<br /> |


#### NetworkAcceleration



NetworkAcceleration defines the configuration for network acceleration features.



_Appears in:_
- [OperatorConfiguration](#operatorconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `autoMNNVLEnabled` _boolean_ | AutoMNNVLEnabled indicates whether automatic MNNVL (Multi-Node NVLink) support is enabled.<br />When enabled, the operator will automatically create and manage ComputeDomain resources<br />for GPU workloads. If the cluster doesn't have the NVIDIA DRA driver installed,<br />the operator will exit with a non-zero exit code.<br />Default: false |  |  |




#### PodCliqueControllerConfiguration



PodCliqueControllerConfiguration defines the configuration for the PodClique controller.



_Appears in:_
- [ControllerConfiguration](#controllerconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `concurrentSyncs` _integer_ | ConcurrentSyncs is the number of workers used for the controller to concurrently work on events. |  |  |


#### PodCliqueScalingGroupControllerConfiguration



PodCliqueScalingGroupControllerConfiguration defines the configuration for the PodCliqueScalingGroup controller.



_Appears in:_
- [ControllerConfiguration](#controllerconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `concurrentSyncs` _integer_ | ConcurrentSyncs is the number of workers used for the controller to concurrently work on events. |  |  |


#### PodCliqueSetControllerConfiguration



PodCliqueSetControllerConfiguration defines the configuration for the PodCliqueSet controller.



_Appears in:_
- [ControllerConfiguration](#controllerconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `concurrentSyncs` _integer_ | ConcurrentSyncs is the number of workers used for the controller to concurrently work on events. |  |  |


#### Server



Server contains information for HTTP(S) server configuration.



_Appears in:_
- [ServerConfiguration](#serverconfiguration)
- [WebhookServer](#webhookserver)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `bindAddress` _string_ | BindAddress is the IP address on which to listen for the specified port. |  |  |
| `port` _integer_ | Port is the port on which to serve requests. |  |  |


#### ServerConfiguration



ServerConfiguration defines the configuration for the HTTP(S) servers.



_Appears in:_
- [OperatorConfiguration](#operatorconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `webhooks` _[WebhookServer](#webhookserver)_ | Webhooks is the configuration for the HTTP(S) webhook server. |  |  |
| `healthProbes` _[Server](#server)_ | HealthProbes is the configuration for serving the healthz and readyz endpoints. |  |  |
| `metrics` _[Server](#server)_ | Metrics is the configuration for serving the metrics endpoint. |  |  |


#### TopologyAwareSchedulingConfiguration



TopologyAwareSchedulingConfiguration defines the configuration for topology-aware scheduling.



_Appears in:_
- [OperatorConfiguration](#operatorconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled indicates whether topology-aware scheduling is enabled. |  |  |
| `levels` _[TopologyLevel](#topologylevel) array_ | Levels is an ordered list of topology levels from broadest to narrowest scope.<br />Used to create/update the TopologyAwareScheduling CR at operator startup. |  |  |


#### WebhookServer



WebhookServer defines the configuration for the HTTP(S) webhook server.



_Appears in:_
- [ServerConfiguration](#serverconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `bindAddress` _string_ | BindAddress is the IP address on which to listen for the specified port. |  |  |
| `port` _integer_ | Port is the port on which to serve requests. |  |  |
| `serverCertDir` _string_ | ServerCertDir is the directory containing the server certificate and key. |  |  |
| `secretName` _string_ | SecretName is the name of the Kubernetes Secret containing webhook certificates.<br />The Secret must contain tls.crt, tls.key, and ca.crt. | grove-webhook-server-cert |  |
| `certProvisionMode` _[CertProvisionMode](#certprovisionmode)_ | CertProvisionMode controls how webhook certificates are provisioned. | auto | Enum: [auto manual] <br /> |


