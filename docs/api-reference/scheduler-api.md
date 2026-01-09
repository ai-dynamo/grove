# API Reference

## Packages
- [scheduler.grove.io/v1alpha1](#schedulergroveiov1alpha1)


## scheduler.grove.io/v1alpha1


### Resource Types
- [PodGang](#podgang)



#### NamespacedName



NamespacedName is a struct that contains the namespace and name of an object.
types.NamespacedName does not have json tags, so we define our own for the time being.
If https://github.com/kubernetes/kubernetes/issues/131313 is resolved, we can switch to using the APIMachinery type instead.



_Appears in:_
- [PodGangSpec](#podgangspec)
- [PodGroup](#podgroup)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `namespace` _string_ | Namespace is the namespace of the object. |  |  |
| `name` _string_ | Name is the name of the object. |  |  |


#### PodGang



PodGang defines a specification of a group of pods that should be scheduled together.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `scheduler.grove.io/v1alpha1` | | |
| `kind` _string_ | `PodGang` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[PodGangSpec](#podgangspec)_ | Spec defines the specification of the PodGang. |  |  |
| `status` _[PodGangStatus](#podgangstatus)_ | Status defines the status of the PodGang. |  |  |




#### PodGangPhase

_Underlying type:_ _string_

PodGangPhase defines the current phase of a PodGang.



_Appears in:_
- [PodGangStatus](#podgangstatus)

| Field | Description |
| --- | --- |
| `Pending` | PodGangPhasePending indicates that all the pods in a PodGang have been created and the PodGang is pending scheduling.<br /> |
| `Starting` | PodGangPhaseStarting indicates that the scheduler has started binding pods in the PodGang to nodes.<br /> |
| `Running` | PodGangPhaseRunning indicates that all the pods in the PodGang have been scheduled and are running.<br /> |


#### PodGangSpec



PodGangSpec defines the specification of a PodGang.



_Appears in:_
- [PodGang](#podgang)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `podgroups` _[PodGroup](#podgroup) array_ | PodGroups is a list of member pod groups in the PodGang. |  |  |
| `topologyConstraint` _[TopologyConstraint](#topologyconstraint)_ | TopologyConstraint defines topology packing constraints for entire pod gang.<br />Translated from PodCliqueSet.TopologyConstraint.<br />Updated by operator on each reconciliation when PodCliqueSet topology constraints change. |  |  |
| `topologyConstraintGroupConfigs` _[TopologyConstraintGroupConfig](#topologyconstraintgroupconfig) array_ | TopologyConstraintGroupConfigs defines TopologyConstraints for a group of PodGroups when it is a strict subset<br />of total number of PodGroups for topology-aware placement. |  |  |
| `priorityClassName` _string_ | PriorityClassName is the name of the PriorityClass for the PodGang. |  |  |
| `reuseReservationRef` _[NamespacedName](#namespacedname)_ | ReuseReservationRef holds the reference to another PodGang resource scheduled previously.<br />During updates, an operator can suggest to reuse the reservation of the previous PodGang for a newer version of the<br />PodGang resource. This is a suggestion for the scheduler and not a requirement that must be met. If the scheduler plugin<br />finds that the reservation done previously was network optimised and there are no better alternatives available, then it<br />will reuse the reservation. If there are better alternatives available, then the scheduler will ignore this suggestion. |  |  |


#### PodGangStatus



PodGangStatus defines the status of a PodGang.



_Appears in:_
- [PodGang](#podgang)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[PodGangPhase](#podgangphase)_ | Phase is the current phase of a PodGang. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.33/#condition-v1-meta) array_ | Conditions is a list of conditions that describe the current state of the PodGang. |  |  |
| `placementScore` _float_ | PlacementScore is network optimality score for the PodGang. If the choice that the scheduler has made corresponds to the<br />best possible placement of the pods in the PodGang, then the score will be 1.0. Higher the score, better the placement. |  |  |


#### PodGroup



PodGroup defines a set of pods in a PodGang that share the same PodTemplateSpec.



_Appears in:_
- [PodGangSpec](#podgangspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the PodGroup. |  |  |
| `podReferences` _[NamespacedName](#namespacedname) array_ | PodReferences is a list of references to the Pods that are part of this group. |  |  |
| `minReplicas` _integer_ | MinReplicas is the number of replicas that needs to be gang scheduled.<br />If the MinReplicas is greater than len(PodReferences) then scheduler makes the best effort to schedule as many pods beyond<br />MinReplicas. However, guaranteed gang scheduling is only provided for MinReplicas. |  |  |
| `topologyConstraint` _[TopologyConstraint](#topologyconstraint)_ | TopologyConstraint defines topology packing constraints for this PodGroup.<br />Enables PodClique-level topology constraints.<br />Updated by operator when PodClique topology constraints change. |  |  |


#### TopologyConstraint



TopologyConstraint defines topology packing constraints with required and preferred levels.



_Appears in:_
- [PodGangSpec](#podgangspec)
- [PodGroup](#podgroup)
- [TopologyConstraintGroupConfig](#topologyconstraintgroupconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `packConstraint` _[TopologyPackConstraint](#topologypackconstraint)_ | PackConstraint defines topology packing constraint with required and preferred levels.<br />Operator translates user's level name to corresponding topologyKeys. |  |  |


#### TopologyConstraintGroupConfig



TopologyConstraintGroupConfig defines topology constraints for a group of PodGroups.



_Appears in:_
- [PodGangSpec](#podgangspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the topology constraint group. |  |  |
| `podGroupNames` _string array_ | PodGroupNames is the list of PodGroup names in the topology constraint group. |  |  |
| `topologyConstraint` _[TopologyConstraint](#topologyconstraint)_ | TopologyConstraint defines topology packing constraints for this group. |  |  |


#### TopologyPackConstraint



TopologyPackConstraint defines a topology packing constraint.
Each of Required and Preferred fields hold a topologyKey, e.g. "kubernetes.io/hostname" ( these are key of labels added on nodes).



_Appears in:_
- [TopologyConstraint](#topologyconstraint)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `required` _string_ | Required defines a topology constraint that must be satisfied as a hard requirement. The workload will not be<br />scheduled if this constraint cannot be satisfied. Generally, it is easier for the scheduler to satisfy constraints<br />on topology domains with larger compute capacity, (e.g. zone or datacenter), than smaller domains, (e.g. host or<br />numa). Holds topologyKey (not level name) translated from user's packLevel specification.<br />Example: "topology.kubernetes.io/rack" |  |  |
| `preferred` _string_ | Preferred defines best-effort topology constraint. Topology domains that provide the most optimized performance<br />with dense packing (e.g. host or numa) are typically used as preferred constraints for topology packing. It might be<br />harder to satisfy these constraints if the topology domains are limited in compute capacity. Since it is preferred<br />constraint, it is therefore not binding on the scheduler to mandatorily satisfy this packing constraint. Scheduler<br />can fall back to higher topology levels (upto Required constraint) if preferred cannot be satisfied.<br />Example: "kubernetes.io/hostname" |  |  |


