# GREP-369: Support Multiple Topologies

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Heterogeneous GPU Clusters](#story-1-heterogeneous-gpu-clusters)
    - [Story 2: Multi-Cloud Clusters](#story-2-multi-cloud-clusters)
    - [Story 3: Topology Retry Before Scheduling](#story-3-topology-retry-before-scheduling)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [Monitoring](#monitoring)
  - [Dependencies (<em>Optional</em>)](#dependencies-optional)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History (<em>Optional</em>)](#implementation-history-optional)
- [Alternatives (<em>Optional</em>)](#alternatives-optional)
- [Appendix (<em>Optional</em>)](#appendix-optional)
<!-- /toc -->

<!--
Include a table of contents as it helps to navigate easily in the document.

Ensure the TOC is wrapped with
   <code>&lt;!-- toc --&gt;&lt;!-- /toc --&gt;</code>
tags, and then generate by invoking the make target `update-toc`.

-->

## Summary

Heterogeneous clusters may contain hardware with different topology characteristics, such as multiple GPU architectures or nodes spanning multiple cloud providers. This GREP proposes extending the Grove topology API to support multiple named topologies, allowing workloads to specify which topology applies to them.

## Motivation

Grove's topology API currently supports a single cluster-wide topology defined in the OperatorConfiguration. This works well for homogeneous clusters but becomes limiting when infrastructure diversity increases.

AI clusters often contain heterogeneous hardware with different interconnect characteristics, or span multiple cloud environments. Each hardware type or environment may require a distinct topology definition for optimal scheduling.

Enabling multiple topologies allows:

- **Accurate Infrastructure Modeling**: Administrators can define topologies matching their actual hardware rather than using a single approximation.
- **Workload Portability**: Users can target specific topologies without embedding infrastructure details into workload specifications.

### Goals

- Define a mechanism for creating multiple named topology resources within a single cluster
- Extend the PodCliqueSet API to reference a specific topology
- Provide default topology selection when none is explicitly specified
- Maintain backward compatibility with existing deployments

### Non-Goals

- Automatic topology discovery or inference from node labels
- Dynamic topology switching for running workloads
- Cross-topology scheduling within a single PodCliqueSet

## Proposal

This GREP extends the existing topology API to support multiple ClusterTopology resources within a single cluster, and adds a reference mechanism for PodCliqueSets to select which topology applies.

The existing `TopologyConstraint` API, where users specify topology domains (e.g., "rack", "host"), remains unchanged. What changes is how the operator resolves which ClusterTopology to use when translating those domain names to infrastructure-specific node label keys.

The approach has four parts:

1. **Multiple ClusterTopology resources**: Remove the restriction to a single operator-managed ClusterTopology. Allow administrators to create multiple named ClusterTopology resources, each describing a different topology hierarchy.

2. **Topology reference on PodCliqueSet**: Add a field to PodCliqueSet that references a specific ClusterTopology by name. This determines which topology definition is used for scheduling.

3. **Default topology**: When no topology is explicitly referenced, a designated default topology is used, preserving backward compatibility.

4. **Mutable topology reference until scheduling**: The topology reference on a PodCliqueSet can be changed while the workload is not yet running, allowing users to retry scheduling against a different topology. Once the gang starts running, the topology reference becomes immutable and updates are rejected.

### User Stories

#### Story 1: Heterogeneous GPU Clusters

As a cluster administrator managing a cluster with different GPU architectures, I want to define separate topologies for each architecture so that workloads are scheduled with topology awareness appropriate to the hardware they're running on.

#### Story 2: Multi-Cloud Clusters

As a platform engineer managing AI workloads on a cluster spanning multiple cloud environments, I want to define environment-specific topologies so that workloads get optimal placement regardless of where they run, without users needing to know the underlying infrastructure details.

#### Story 3: Topology Retry Before Scheduling

As a user submitting a PodCliqueSet to a cluster with multiple scheduling shards, I want to be able to change the target topology while my workload is pending, so I can retry on a different shard if the first one cannot accommodate my gang. Once the gang starts running, the topology should be locked.

### Limitations/Risks & Mitigations

<!-- 
What are the current set of limitations or risks of this proposal? Think broadly by considering the impact of the changes proposed on kubernetes ecosystem. Optionally mention ways to mitigate these.
-->

## Design Details

<!-- 
This section may include API specifications (GO API/YAML) and certain flow control diagrams that will help reviewers to know how the proposal will be implemented.
-->

### Monitoring

<!--
This section contains details of events, metrics, status conditions and other status fields that will aid in determining health of the feature, or help measure any service level objectives that might be optionally defined.
-->

### Dependencies (*Optional*)

<!--
Are there any dependencies for this feature to work? If yes then those should be clearly listed with optional links on how to ensure that the dependencies are setup.
-->

### Test Plan

<!--
For the functionality an epic (issue) should be created. Along with a sub-issue for the GREP, there should be a dedicated issue created for integration and e2e tests. This issue should have details of all scenarios that needs to be tested. Provide a link to issue(s) in this section.
-->

### Graduation Criteria

<!-- 
In this section graduation milestones should be defined. The progression of the overall feature can be evaluated w.r.t API maturity, staged sub-feature implementation or some other criteria.

In general we try to use the same stages (alpha, beta, GA), regardless of how the
functionality is accessed. Refer to these for more details:"

* [Feature Gates](https://git.k8s.io/community/contributors/devel/sig-architecture/feature-gates.md)
* [Maturity levels](https://git.k8s.io/community/contributors/devel/sig-architecture/api_changes.md#alpha-beta-and-stable-versions)
* [Deprecation Policy](https://kubernetes.io/docs/reference/using-api/deprecation-policy/ ) 

**Note:** Generally we also wait at least two releases between beta and
GA/stable, because there's no opportunity for user feedback, or even bug reports,
in back-to-back releases. 
-->

## Implementation History (*Optional*)

<!--
Major milestones in the lifecycle of a GREP should be tracked in this section.
Major milestones might include:

- The date proposal was accepted and merged.
- The date implementation started.
- The date of Alpha release for the feature.
- The date the feature graduated to beta/GA

-->

## Alternatives (*Optional*)

<!--
What are the alternative approaches considered and reasons to rule those out. This section should have sufficient details (not too much) to express the alternative idea and why it was not accepted.
-->

## Appendix (*Optional*)

<!-- 
Use this section to put any prerequisite reading links or helpful information/data that supplements the proposal, thus providing additional context to the reviewer.
-->

> NOTE: This GREP template has been inspired by [KEP Template](https://github.com/kubernetes/enhancements/blob/f90055d254c356b2c038a1bdf4610bf4acd8d7be/keps/NNNN-kep-template/README.md).

