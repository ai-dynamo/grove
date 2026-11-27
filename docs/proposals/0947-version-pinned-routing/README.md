# GREP-0947: Version-Pinned Routing for Coherent Updates

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [API and Strategy Behavior](#api-and-strategy-behavior)
  - [Routing Label Contract](#routing-label-contract)
  - [Controller Flow](#controller-flow)
  - [Snapshot Selection and Materialization](#snapshot-selection-and-materialization)
  - [Rollout Identity and Completion](#rollout-identity-and-completion)
  - [Enabling and Disabling](#enabling-and-disabling)
  - [Corrective Edits and Retained Revisions](#corrective-edits-and-retained-revisions)
  - [Monitoring](#monitoring)
  - [Dependencies and Review Decisions](#dependencies-and-review-decisions)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This increment to [GREP-393](../393-coherent-rolling-updates/README.md) proposes that Grove label each Pod it creates for a PodCliqueSet with the generation hash of the templates used to create it. The label is independent of update policy. An operator using Coherent updates can also select `AllComponents` to replace unchanged components during a rollout. Selective replacement remains the default.

## Motivation

Today, `grove.io/pod-template-hash` identifies the template of one clique. Prefill and decode therefore have different values even when Grove creates them from the same PodCliqueSet (PCS) configuration.

Consider a decode-only update from R1 to R2. With a PCS-wide label and selective replacement, the existing prefill Pods would keep R1 and the new decode Pods would carry R2. Once the last R1 decode Pod leaves, a router requiring a matching pair could find neither revision complete. An operator who needs that pairing can opt to replace prefill as part of the same rollout.

Here, a **snapshot** is the PCS configuration that Grove selects to create a Pod. The label identifies the set of clique Pod templates in that configuration using Grove's existing `PodCliqueSetGenerationHash`. [#941](https://github.com/ai-dynamo/grove/issues/941) tracks how to recover the selected configuration during an update.

### Goals

- Give each new Pod a shared, Grove-owned template identity within its PCS, including during initial deployment, scale-out, and recovery.
- Keep the creation-time value on a surviving Pod across updates and controller restarts.
- Make all-component replacement explicit and opt-in for Coherent updates, including unchanged standalone cliques and scaling groups.
- Preserve Coherent planning, disruption budgets, and the default selective behavior.

### Non-Goals

- Inferring binary, protocol, model, or KV-cache compatibility from a revision label.
- Guaranteeing uninterrupted service, adding surge capacity, or draining in-flight requests.
- Relabeling unchanged Pods into multiple compatible revisions or defining revision membership sets.
- Adding a rollback API or changing which Pods `RollingRecreate` and `OnDelete` replace.
- Specifying router, gateway, or llm-d overlay behavior. Request pinning, admission errors, and retries belong to the router integration tracked under #947.
- Implementing the revision-persistence work tracked by [#941](https://github.com/ai-dynamo/grove/issues/941) in this documentation PR.

## Proposal

Grove assigns the routing label when it creates a Pod under any update policy. When a Pod survives an update, its label continues to describe the configuration that created it.

An operator may opt a Coherent PodCliqueSet into all-component replacement. Every rollout in that mode includes all standalone PodCliques and all PodCliqueScalingGroups, even when only one clique's template changed. Existing MVU, anchor/tail, replica ordering, and disruption-budget rules still determine the replacement cadence.

### User Stories

- As an operator of a disaggregated inference workload, I want a decode-only update to replace prefill as well, so that a router can find prefill and decode Pods that were created from the same PodCliqueSet configuration.
- As an llm-d operator, I want the [rollout screener](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/epp/framework/plugins/requesthandling/screener/disaggregatedsetrollout/README.md) to use `grove.io/routing-revision`, so each request can stay on one revision. If the pinned revision loses its last Ready decode endpoint after prefill, the attempt should fail instead of switching revisions.
- As an operator who does not need version-pinned routing, I want Coherent updates to keep replacing only changed components, so that I do not pay restart costs for unchanged roles.

### Limitations/Risks & Mitigations

- All-component replacement also restarts unchanged roles. Operators still need to size capacity and disruption budgets for their workloads.
- A selective rollout can leave different roles on different hashes indefinitely. Even with all-component replacement, some stages of the rollout can lack a complete set of Ready endpoints.
- An old child spec can coexist with a newer PCS target. Grove must obtain the Pod configuration and its hash from the same selection before creating the Pod.
- Mutable image tags, external configuration, and application state can change while the template hash stays the same. Operators who need immutable workload inputs must pin them separately.

## Design Details

### API and Strategy Behavior

This example selects all-component replacement on a v1beta1 PodCliqueSet.

```yaml
apiVersion: grove.io/v1beta1
kind: PodCliqueSet
metadata:
  name: inference
spec:
  updateStrategy:
    type: Coherent
    updateScope: AllComponents
  # replicas and template omitted
```

Omitting `updateScope` keeps Coherent replacement selective. Both settings, as well as `RollingRecreate` and `OnDelete`, label newly created Pods. Existing update policies retain their replacement behavior. The API spelling and the representation of the optional setting across served API versions remain open.

### Routing Label Contract

The proposed Pod label key is `grove.io/routing-revision`.

- Grove writes the `PodCliqueSetGenerationHash` from the revision selected for Pod creation. [Grove's hash calculation](https://github.com/ai-dynamo/grove/blob/15411d9ccb37cfbae9e7cded612762e1ad41777c/operator/internal/controller/podcliqueset/reconcilespec.go#L124-L138) covers each clique's labels, annotations, and PodSpec, with the PCS priority class applied. A scale, budget, or update-scope edit alone leaves those inputs unchanged.
- Consumers compare values within the same PCS instance. The hash identifies the template set. Two snapshots with the same hash inputs can share the value, including after a rollback or recreation of a revision record. Consumers cannot derive a rollout sequence or the identity of every dependent resource from it.
- Each Pod keeps its creation-time label until deletion. A later change of scope, strategy, or PodGang membership leaves that Pod's value intact. Pods that predate the feature can remain unlabeled until replacement. An absent value cannot be used to match two Pods.
- Grove owns the label. Template-supplied metadata must not override the value selected by the controller.

### Controller Flow

| Step | Current behavior | Proposed change |
| --- | --- | --- |
| PCS API | Carries the update strategy. | Express the optional all-component replacement scope for Coherent updates. Label creation needs no opt-in or scope-transition validation. |
| PCS reconciler (`computeCoherentUpdateScope`) | Selects components whose clique templates changed and carries forward pending scope. | For an all-component rollout, include every standalone PodClique and PodCliqueScalingGroup, including unchanged components. |
| PodGangMap planner | Orders replacement by MVU, anchor/tail, and budgets. Counts entries at the target hash as committed. | Apply those ordering and budget rules to the expanded scope. Credit a required replacement when this rollout emits its step, including when a hash recurs. |
| PCS child components and Pod builder | Pod construction uses the parent PodClique's PodSpec. The PodGangMap entry tracks the generation that the planner expects. | Carry the hash together with the selected configuration and stamp it on the new Pod under every policy. |
| Status (`mutateUpdatedReplica`) | Counts updated Pods by clique template hash. | During all-component replacement, account for unchanged cliques whose Pods still need replacement. See [Rollout Identity and Completion](#rollout-identity-and-completion). |

### Snapshot Selection and Materialization

The selected PCS revision supplies both the Pod configuration and its generation hash. Reading the latest PCS status or a live PodGangMap entry later can misidentify an existing Pod. In a selective Coherent update, the [planner advances a nonempty entry](https://github.com/ai-dynamo/grove/blob/15411d9ccb37cfbae9e7cded612762e1ad41777c/operator/internal/controller/podcliqueset/components/podgangmap/coherentupdate.go#L109-L122) once only out-of-scope components remain in it. Their surviving Pods still carry their original hashes.

During an update, old and new cohorts can coexist within a replica. If Grove creates a replacement or recreates a child after a restart, it must recover the configuration selected for that cohort. When an old child and a new target disagree and Grove cannot establish which configuration to use, it retries before creating the Pod. The revision-persistence work in [#941](https://github.com/ai-dynamo/grove/issues/941) and [#912](https://github.com/ai-dynamo/grove/pull/912) can supply that history. The UID or retention lifetime of a revision record does not determine the Pod label.

Existing suppression of Pod creation on draining old-generation anchors still applies. Scaling-group members, inactive replicas, and dependent resources must continue to use their selected configuration during recovery.

### Rollout Identity and Completion

With `AllComponents`, an unchanged prefill template can still require new Pods because the decode template changed. For an R1-to-R2 rollout, a prefill Pod created with R1 cannot count toward an R2 replacement. The controller must account for the planned replacements in updated and updated-scheduled counts, child convergence, and PCS completion. It continues to apply existing scheduling, readiness, and disruption-budget rules.

Suppose a corrective edit restores R1 while an original R1 entry survives an incomplete R2 rollout. Today [`countReplicasAtCurrentHash`](https://github.com/ai-dynamo/grove/blob/15411d9ccb37cfbae9e7cded612762e1ad41777c/operator/internal/controller/podcliqueset/components/podgangmap/substepplanner.go#L289-L364) credits that entry toward the new R1 plan before it emits a replacement. If those counts meet the target, the planner can return no next step.

For an all-component rollout, the planner must distinguish entries carried over from earlier rollouts from replacement work committed by the current rollout. Use that same distinction to recover plan position, select anchors and replicas to drain, and check whether the current batch is scheduled. Status accounting and Pod creation or deletion for that plan must follow the same assignments after a restart. How Grove persists that progress is an implementation decision. Both the surviving and replacement Pods still expose the same R1 template hash.

The [disruption budget](https://github.com/ai-dynamo/grove/blob/15411d9ccb37cfbae9e7cded612762e1ad41777c/operator/internal/controller/podcliqueset/components/podgangmap/coherentupdate.go#L318-L347) subtracts replacements that are in flight and unready. With `MaxUnavailable=1`, classifying one unready survivor as a new R1 replacement can use the whole budget before the planner replaces anything. For both standalone PodCliques and scaling groups, an old Pod or replica matching the target hash remains eligible for replacement under the existing capacity rules. While a replacement is in flight and unready, its disruption still counts across corrective edits and restarts.

A scale-only edit keeps the hash and does not force replacement. Direct scaling of PodClique and PodCliqueScalingGroup children remains blocked during a Coherent update, as specified by [GREP-393](../393-coherent-rolling-updates/README.md#handling-scale-outs-and-scale-ins-during-update). When existing rules allow scale-out, new Pods use the hash selected for their replica or cohort.

### Enabling and Disabling

On a converged PCS, removing `AllComponents` leaves existing Pods running with their original labels. The next Coherent update uses selective replacement. Any Pod created after the change still receives its creation-time hash. Disabling the setting requires no final rollout to clear labels.

Enabling `AllComponents` selects every component for a Coherent rollout. Changing the scope alone leaves the hash unchanged. Should enabling it immediately replace Pods with mixed or missing labels when the templates have not changed? If a rollout is already in progress, which scope applies to the remaining work? Both planning questions are open. Surviving Pods keep their labels in either case.

### Corrective Edits and Retained Revisions

Continue accepting corrective template edits during an unsuccessful rollout. During an all-component update, a new target R3 includes still-pending R1 components and partially replaced R2 components. Pods keep their original labels until replaced. The planner still gates progress on scheduling and charges in-flight replacements against disruption budgets.

Restoring exactly the template inputs that produced R1 produces the R1 hash, even if the stored revision was recreated. A recreation still needs the correct configuration. Keeping that configuration available while a live cohort or pending replacement depends on it belongs to revision persistence.

### Monitoring

Users can inspect Pod labels alongside each Pod's role and readiness. Existing PCS `UpdateProgress` and component counts expose rollout progress. When snapshot selection blocks creation, logs or events should identify the PCS and affected replica or cohort. Router availability telemetry belongs to the data plane.

### Dependencies and Review Decisions

- On Grove main, [`computeCoherentUpdateScope`](https://github.com/ai-dynamo/grove/blob/15411d9ccb37cfbae9e7cded612762e1ad41777c/operator/internal/controller/podcliqueset/coherentupdatescope.go#L34-L53) calculates selective scope, and [`mutateUpdatedReplica`](https://github.com/ai-dynamo/grove/blob/15411d9ccb37cfbae9e7cded612762e1ad41777c/operator/internal/controller/podclique/reconcilestatus.go#L153-L185) counts Pods at the clique template hash. All-component replacement extends both paths.
- [`LabelPodCliqueSetGenerationHash`](https://github.com/ai-dynamo/grove/blob/15411d9ccb37cfbae9e7cded612762e1ad41777c/operator/api/common/labels.go#L38-L41) is currently a deprecated **PodGang** label. This proposal reuses the generation hash value and gives each **Pod** its own creation-time copy. The exact public Pod label key is a review decision.
- [#912](https://github.com/ai-dynamo/grove/pull/912) proposes storing [PCS template snapshots](https://github.com/ai-dynamo/grove/blob/64fd66153854d310921012c303cdb097b52f92f9/operator/internal/utils/controllerrevision/revision.go). The interface that supplies a Pod's selected revision and recovers it after a restart remains open with #941.
- The scope's API spelling, round-trip behavior for the still-served v1alpha1 API, and the two planning cases under [Enabling and Disabling](#enabling-and-disabling) need agreement before implementation.

The [#947 discussion](https://github.com/ai-dynamo/grove/issues/947#issuecomment-6796887804) tracks llm-d router and Grove-overlay integration separately.

### Test Plan

These are acceptance criteria for the future implementation.

**Unit and controller tests**

- Check the creation-time label for default and explicit `RollingRecreate`, `OnDelete`, selective `Coherent`, and `Coherent` with `AllComponents`. Exercise initial deployment, permitted scale-out, restart, and replacement. Leave pre-feature Pods unlabeled and prevent user metadata from overriding the controller's value.
- Cover a decode-only update under both Coherent scopes, including standalone cliques, scaling groups, zero replicas, and pending work. Under selective replacement, an unchanged prefill Pod retains its UID and R1 label even if its PodGangMap entry advances to R2. Under all-component replacement, prefill gets a new UID and the same R2 hash as decode.
- Remove `AllComponents` on a converged PCS. Verify no Pod UID or label changes from that edit, a subsequent decode-only update remains selective, and newly created Pods still carry the right hash. Check that a change to scope, replica count, or disruption budget alone keeps the hash. Exercise served-version round trips and the scope-only enable and active-rollout cases once their behavior is agreed.
- Exercise stale children and old/new cohorts, missing or foreign snapshots, controller restarts, active and inactive replica recreation, and the existing suppression of old-anchor creates. Verify the PodSpec as well as its label. Test R1/R2/R3 corrections and a return to identical R1 inputs, including recreation of a revision record.
- Interrupt an R2 rollout with an original R1 entry still present, then retarget to R1. Assert that the planner emits the required replacement step, resumes it after restart without scheduling it twice, and eventually observes a new Pod UID with the same R1 label. Check updated and scheduled counts against the plan for unchanged cliques, terminating Pods, and scheduled-but-unready targets.
- Repeat the rollback with one unready survivor, another Ready replica, and a budget of one for each component type. Verify that the survivor's matching hash does not consume the in-flight replacement allowance. Once its replacement is committed but still unready, verify that the budget blocks taking down the Ready replica.

**Integration and scheduler E2E**

Extend `operator/e2e/tests/update/coherentupdate_test.go` and revision-persistence tests with a disaggregated workload containing standalone and scaling-group roles. Verify Pod UIDs, actual configuration, and labels across selective and all-component updates, scale-out, disablement, and resource loss. Exercise unschedulable and scheduled-but-unready targets and confirm that corrective edits respect disruption budgets. Preserve existing replacement behavior under `RollingRecreate` and `OnDelete`. Track implementation and router-side tests under #947.

### Graduation Criteria

- **Alpha** requires an agreed creation-time label contract, API representation, unit/controller coverage, and scheduler E2E for both rollout scopes.
- **Beta** requires stable conversion and restart/recreation behavior, validated scope changes, failed-target corrections, and operator guidance on incomplete role sets.
- **GA** requires at least two releases of beta feedback, stable label and API behavior, and maintained upgrade and regression coverage.

## Alternatives

- **PCS metadata generation.** Changes such as scaling can advance object generation while the template set stays the same.
- **ControllerRevision UID.** Recreating a stored record can assign a different UID to identical templates. The existing PCS template hash provides the identity required here.
- **Relabel out-of-scope Pods.** A surviving prefill Pod may be compatible with more than one decode revision. A single creation label records only the template set that created that Pod.
- **Always replace all components.** This imposes restart costs on workloads that use selective updates.
- **User-maintained revision labels.** This works today if users consistently label every role when its configuration changes.
