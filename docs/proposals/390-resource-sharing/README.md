# Resource Sharing Amongst Pods

<!-- toc -->

- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (<em>Optional</em>)](#user-stories-optional)
    - [Story 1 (<em>Optional</em>)](#story-1-optional)
    - [Story 2 (<em>Optional</em>)](#story-2-optional)
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

## Summary

<!--
This section should contain user-focused documentation such as release notes or a development roadmap. It should also have a summary of functional increment that is being introduced as part of this GREP. A good summary should address a wider audience and should ideally be a single paragraph.
-->
Grove API supports primitives that allow the application to be deployed and scaled through multiple levels of grouping of pods.
The purpose of this GREP is to enable pod groups defined through Grove primitives--PodCliques, PodCliqueScalingGroup and PodCliqueSet--share cluster resources within replicas of each level of grouping while dynamically forming sharing groups at level when scaling.
In Kubernetes, the ResourceClaim and ResourceClaimTemplate APIs, created as part of the DRA feature, enable resource sharing.
This proposal aims to leverage these APIs in the context of Grove to enable multi-level resource sharing.

## Motivation

<!-- 
This section explicitly lists the motivation which consists of goals and the non-goals of this GREP. Use this section to describe why the change introduced in this GREP is important and what benefits it brings to the users.
-->
Dynamo wants to create shadow pods on every active pod that share the same GPUs to enable quick recovery from faults.
If a ResourceClaim is requested by the pod template of a PodClique, it means, every pod created out of that template will reference that same claim.
This does not work as a PodClique that is part of PodCliqueScalingGroup gets instantiated for every replica of the PCSG, or even the PodCliqueSet.
On the other hand, using a ResourceClaimTemplate on the pod template means that every pod gets to use a different resource claim.
Hence, Grove needs to enable sharing of resources within a PodClique or replica of a PodCliqueScalingGroup, but ensure isolation across every instantiation of the PodClique.

### Goals

<!-- 
Lists specific goals of this GREP. What is it trying to achieve.
-->
- Enable users to define resource sharing primitives at multiple levels of Grove hierarchy, i.e. PodClique, PodCliqueScalingGroup and PodCliqueSet
- Users should be able to limit and scope resource sharing within subset of a group or within a specific level,
e.g. share resource between pods of a PodClique instance vs between pods of a PCSG instance, or between a subset of PCLQs within a PCSG instance.
- Enable users to reference externally created ResourceClaimTemplates to be used for a sharing group
- Enable users to define their own ResourceClaimSpec into PodCliqueSet API so that Grove can create and manage the lifecycle of resource claims and resource claim templates.


### Non-Goals

<!-- 
Lists what all is out of scope of this GREP. Listing non-goals helps to clearly define the scope within which the discussions should happen.
-->

## Proposal

<!-- 
Contains the specifics of the proposal. Sufficient details should be provided to help reviewers clearly understand the proposal. It should not include API design, low level design and implementation details which should be mentioned under 'Design Details' section instead.
-->

### User Stories (*Optional*)

<!-- 
This section provides detailed use cases descibing how changes introduced in this GREP are going to be used. The intent is to carve out real-world scenarios which serve as additional motivation providing clarity/context for the changes proposed.
-->

#### Story 1 (*Optional*)

#### Story 2 (*Optional*)

### Limitations/Risks & Mitigations

<!-- 
What are the current set of limitations or risks of this proposal? Think broadly by considering the impact of the changes proposed on kubernetes ecosystem. Optionally mention ways to mitigate these.
-->

## Design Details



```go
type PodCliqueTemplateSpec struct {
	// Name must be unique within a PodCliqueSet and is used to denote a role.
	// Once set it cannot be updated.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/names#names
	Name string `json:"name"`
  ...
	// ResourceClaimTemplateNames is a list of resource.ResourceClaimTemplate names which will be used to create
	// ResourceClaims that are added to the PodSpec of each core.Pod in the PodClique instance, thus allowing sharing of
	// resources such as accelerators across all pods in the PodClique. All ResourceClaims created will be completely
	// managed by Grove.
	// NOTE: This is not the same as adding ResourceClaimTemplate inside the
	// Spec.PodSpec.ResourceClaims[x].ResourceClaimTemplateName in the PodClique since that will create a unique
	// ResourceClaim for each pod in the PodClique.
	ResourceClaimTemplateNames []string `json:"resourceClaimTemplateNames,omitempty"`
	// Specification of the desired behavior of a PodClique.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#spec-and-status
	Spec PodCliqueSpec `json:"spec"`
}
```



```go
// ResourceClaimTemplateConfig defines a common set of resourcev1.ResourceClaimTemplate names for a set of PodCliques.
// A ResourceClaim is created per ResourceClaimTemplate name and added to the PodSpec of each PodClique specified
// in the CliqueNames field. This allows sharing of resources such as accelerators across all pods in the specified
// PodCliques that are part of one PodCliqueScalingGroup instance.
type ResourceClaimTemplateConfig struct {
	// Names is a list of resourcev1.ResourceClaimTemplate names which will be used to create ResourceClaims.
	Names []string `json:"names"`
	// CliqueNames is a list of names of PodCliques that will use the ResourceClaimTemplates specified in the Names field.
	CliqueNames []string `json:"cliqueNames,omitempty"`
}
```

```go
// PodCliqueScalingGroupConfig is a group of PodClique's that are scaled together.
// Each member PodClique.Replicas will be computed as a product of PodCliqueScalingGroupConfig.Replicas and PodCliqueTemplateSpec.Spec.Replicas.
// NOTE: If a PodCliqueScalingGroupConfig is defined, then for the member PodClique's, individual AutoScalingConfig cannot be defined.
type PodCliqueScalingGroupConfig struct {
	// Name is the name of the PodCliqueScalingGroupConfig. This should be unique within the PodCliqueSet.
	// It allows consumers to give a semantic name to a group of PodCliques that needs to be scaled together.
	Name string `json:"name"`
  ...
	// ResourceClaimTemplateConfigs is a list of ResourceClaimTemplateConfig which defines a common set of
	// resourcev1.ResourceClaimTemplate names for a set of PodCliques in the scaling group. A ResourceClaim is created per
	// ResourceClaimTemplate name and added to the PodSpec of each PodClique specified in the CliqueNames field of the
	// scaling group. This allows sharing of resources such as accelerators across all pods in the specified PodCliques
	// that are part of one PodCliqueScalingGroup instance.
	ResourceClaimTemplateConfigs []ResourceClaimTemplateConfig `json:"resourceClaimTemplateConfigs,omitempty"`
}
```







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

 
