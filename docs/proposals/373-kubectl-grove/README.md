# GREP-373: `kubectl-grove` CLI and Agent Skills

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Finding why a rollout is stuck](#story-1-finding-why-a-rollout-is-stuck)
    - [Story 2: Inspecting replica placement](#story-2-inspecting-replica-placement)
    - [Story 3: Collecting diagnostics for a bug report](#story-3-collecting-diagnostics-for-a-bug-report)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [Cluster Access](#cluster-access)
  - [Output](#output)
  - [References](#references)
  - [Commands](#commands)
    - [<code>status</code>](#status)
    - [<code>events</code>](#events)
    - [<code>topology</code>](#topology)
    - [<code>version</code>](#version)
  - [GPU Accounting](#gpu-accounting)
  - [Agent Skills](#agent-skills)
  - [RBAC](#rbac)
  - [Backward Compatibility](#backward-compatibility)
  - [Monitoring](#monitoring)
  - [Dependencies](#dependencies)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [GA](#ga)
- [Implementation History](#implementation-history)
- [Alternatives](#alternatives)
- [Open Questions](#open-questions)
- [Future Work](#future-work)
<!-- /toc -->

## Summary

`kubectl grove` is a read-only kubectl plugin for debugging Grove workloads. It shows where Pod failures affect a workload, gathers events across its hierarchy, and reports where each PodClique landed. Operators install the plugin through Krew and can use the matching Grove agent skills to investigate with kubectl.

## Motivation

A Grove workload spans five resource kinds, and its replicas exist only as name indices. With kubectl, users walk this by hand to answer:

- **Where is the workload unhealthy, and why?** Higher levels report availability, not causes. For example, with a typo in the image registry, PodCliques report `MinAvailableBreached=False`, nothing above flags it, and `ErrImagePull` shows only on Pods.
- **What went wrong across the workload?** `kubectl events --for` covers one object and matches by name, so it mixes a recreated object's events with the earlier object's; name prefixes are ambiguous (`foo` vs `foo-bar`).
- **Where did this replica land?** Checking whether its leader and workers share a rack means finding their PodCliques, following each scheduled Pod to its Node, and reading the rack label named by the workload’s topology binding.

Coding agents can run kubectl, but the data is large and they lack Grove knowledge. `kubectl grove` commands do the joins; the skills carry the knowledge.

### Goals

- Find a workload's deepest unhealthy part, and why, in one command.
- Show a workload's events, grouped and correctly attributed even when objects are recreated.
- Show Pod counts per PodClique and topology domain for a workload or replica, and identify Pods awaiting placement.
- Ship Grove knowledge as skills that match the installed CLI.
- Report CLI–operator version skew and link to the operator's exact source.

### Non-Goals

- **Re-implementing kubectl:** YAML, logs, exec, watches and waits stay in kubectl.
- **Changing cluster state:** `kubectl-grove` never writes; skills only suggest commands.
- **An interactive UI or k9s plugin** ([Alternatives](#alternatives)).
- **Live views:** `--watch` and a Grove-aware `wait` are [Future Work](#future-work).
- **Changes to Grove's APIs, operator or Helm chart.**
- **Tooling for Grove contributors.**
- **Fractional GPUs** (MIG, partitioned, shared): [Future Work](#future-work).

## Proposal

`kubectl-grove` adds four read-only commands under `kubectl grove` and three agent skills. Terms:

- **PCS, PCSG, PCLQ, PG, CTB:** PodCliqueSet, PodCliqueScalingGroup, PodClique, PodGang, ClusterTopologyBinding (short names `pcs`, `pcsg`, `pclq`, `pg`, `ctb`).
- **Ref:** `kind/name` (`pcs/llm-serve`), usable with kubectl; a **replica ref** (`pcs/<name>/<i>`, `pcsg/<name>/<i>`) works only with `kubectl grove`.
- **Subtree:** a ref and everything beneath it.
- **Grove Pod:** a Pod controlled by a PodClique.
- **Binding, level, domain:** a ClusterTopologyBinding lists levels (zone, rack, …), each a node label; a domain is one level value (`rack-a2`).
- **Agent skill:** a `SKILL.md` folder that coding agents load when a task matches.

| Command | Answers | Why not kubectl |
|---|---|---|
| `status [REF]` | What is unhealthy, and why? | Walks four kinds and virtual replicas; rolls up Pod reasons. |
| `events [REF]` | What went wrong? | Needs UID matching across a subtree. |
| `topology [LEVEL] --for REF` | Where did this workload land? Which Pods still need a node? | Joins PodClique membership, Pod assignments and topology labels. |
| `version` | Do CLI and operator match? Where is the source? | Only the binary knows its version. |

| Skill | Use it to | Stories |
|---|---|---|
| `grove-debug` | Diagnose pending, crashing or stuck workloads. | [1](#story-1-finding-why-a-rollout-is-stuck) |
| `grove-topology` | Check topology setup and inspect replica placement. | [2](#story-2-inspecting-replica-placement) |
| `grove-diagnostics` | Collect or analyze a diagnostics bundle. | [3](#story-3-collecting-diagnostics-for-a-bug-report) |

Design rules:

- **Minimal CLI:** commands only for data too large or join-heavy for an agent; skills compose the rest.
- **kubectl-identical access:** same flags, kubeconfig, namespace and auth.
- **Output for people and programs:** tables by default; `-o json` with a JSON Schema per command.
- **Skills inform:** they supply Grove knowledge and suggested steps; the agent chooses what to run.

### User Stories

#### Story 1: Finding why a rollout is stuck

As an on-call engineer with read-only access to the workload namespace, I want to identify the replica blocking a rollout and share the evidence in the incident thread.

`status` leads me from the PodCliqueSet to the failing replica and its worker Pod. The replica’s `events` show that the kubelet failed to pull the worker image and name the image it tried. With `grove-debug`, I can ask for an incident update that names the replica and cites that event. This is the workflow the [v1 acceptance tests](#test-plan) exercise.

#### Story 2: Inspecting replica placement

As a platform operator, I want to see which racks host my decode replicas after a node drain. Each replica’s leader and workers should share a rack.

`kubectl grove topology rack --for pcsg/llm-serve-1-decode` shows the first replica’s leader and two workers in `rack-a1`. It lists the second replica’s three replacement Pods as awaiting placement. I can follow up with `events` for that replica to see what the scheduler reported.

#### Story 3: Collecting diagnostics for a bug report

As a cluster operator, I want a redacted diagnostics bundle, so that I can file a bug or let my agent analyze it offline.

`grove-diagnostics` shows the files in the bundle and prompts me to inspect them locally before sharing. I can analyze the bundle offline. To debug locally against the operator's exact code, I can download the source from the link `kubectl grove version` prints.

### Limitations/Risks & Mitigations

- **Placement depends on Node labels.** `topology` identifies assigned Pods whose Node lacks a topology label separately from Pods still waiting for a node. Reading Nodes and ClusterTopologyBindings requires cluster-scoped permissions ([RBAC](#rbac)).
- **Events expire; nothing is live.** *Mitigation:* `events` notes retention; skills add Pod status, conditions and logs, and rerun `status` when `kubectl wait` times out.
- **Agents pay per token and vary between runs.** *Mitigation:* bounded [output](#output); skills narrow scope and filter with `jq`; [skill acceptance runs](#test-plan) on every skill or CLI change check accuracy.
- **Cluster text is untrusted.** *Mitigation:* `kubectl-grove` never acts on it; skills treat it as data, never as commands; docs recommend read-only credentials for agents.
- **Bundles may hold sensitive data.** The skill excludes Secrets and ConfigMaps and strips environment values. Logs, events and workload arguments can still contain sensitive values. It lists the collected files from `manifest.json` and prompts for local review before sharing.
- **Workload GPU accounting is partial.** `status` warns when it cannot count a device ([GPU Accounting](#gpu-accounting)).
- **Version skew.** Each installed skill records its release in a `GROVE_RELEASE` file ([Agent Skills](#agent-skills)). If that file is missing or its major.minor version differs from the CLI’s, the skill repeats the install instruction and continues. If the CLI and operator differ in major or minor version, the skill names the matching CLI release, continues and notes the mismatch ([version](#version)).

## Design Details

### Cluster Access

The CLI is one binary, `kubectl-grove`, on `$PATH`. Commands resolve their targets, join the results in memory and exit. There are no informers or state between runs.

[`k8s.io/cli-runtime`](https://github.com/kubernetes/cli-runtime) gives it kubectl's flags, kubeconfig loading, namespace defaulting, authentication, discovery and name resolution. Per-command `--context` lets agents target a cluster without changing the user's current context. Requests send the User-Agent `kubectl-grove/<version>` with raised client-side QPS. Commands use cobra.

### Output

Every command takes `-o pretty` (default; color on a terminal) or `-o json`; only the flag chooses. JSON rules:

- **Refs:** every record that names an object has a `ref`; a namespace shared by all records appears once at the top.
- **Flat:** records point to their `parent`; each record type has its own array (`groups[]`, `pods[]`).
- **Same rows:** both formats hide the same things and count them in `omitted`.
- **Deduplicated:** `status` lists each (reason, message) once in `reasons[]`; `events` groups identical events.
- **Predictable:** stable order, integer counts, errors on stderr, no ANSI in JSON.
- **Complete:** JSON has every field; pretty output shows a subset plus notices, so there is no `-o wide`.

Part of the [`status`](#status) example as JSON (one branch, a few fields):

```json
{
  "namespace": "inference",
  "groups": [
    {"ref": "pcsg/llm-serve-1-decode/1", "parent": "pcsg/llm-serve-1-decode", "status": "Unavailable", "reason": "Unschedulable", "count": 3},
    {"ref": "pclq/llm-serve-1-decode-1-worker", "parent": "pcsg/llm-serve-1-decode/1", "status": "Unavailable", "reason": "Unschedulable", "count": 2}
  ],
  "pods": [
    {"ref": "pod/llm-serve-1-decode-1-worker-m4q8z", "parent": "pclq/llm-serve-1-decode-1-worker", "node": null, "reason": "Unschedulable"}
  ],
  "reasons": [
    {"reason": "Unschedulable", "count": 3, "message": "pod group needs 12 nvidia.com/gpu within one rack; max free in any rack: 8"}
  ],
  "omitted": {"healthyGroups": 13, "healthyPods": 11}
}
```

`--schema` prints the command's JSON Schema offline. Schemas are generated from the output types, published, and checked by tests.

### References

| Ref | Selects |
|---|---|
| `pcs/<name>[/<i>]`, `pcsg/<name>[/<i>]` | a PCS or PCSG, or its replica `i` |
| `pclq/<name>` | a PCLQ and its Pods |
| `pg/<name>`, `pod/<name>` | one PodGang or Pod (`events` only) |

For `status` and `events`, omitting the ref selects the namespace, or all namespaces with `-A`. Combining a ref with `-A` is an error. `topology` requires a workload or replica ref.

### Commands

Commands exit 0 on success, even with warnings, and 1 on any error. A missing permission is an error naming the verb, resource and scope, except in two cases, which only warn: unreadable DRA objects (GPU counts fall back to the device plugin) and unreadable operator Pods (`version` reports the operator as `unavailable`).

The examples use this illustrative cluster:

```text
h200-cluster (ClusterTopologyBinding: zone > block > rack > host)
  block-a: rack-a1, rack-a2          block-b: rack-b1, rack-b2
  16 nodes x 8 H200 GPUs; h200-08 (rack-a2) drained and cordoned
llm-serve (PCS, 2 replicas)
  frontend   (PCLQ, 1 Pod, no GPUs)
  decode     (PCSG, 2 replicas, minAvailable 1, each packed in one rack)
    leader   (PCLQ, 1 Pod x 4 GPUs)
    worker   (PCLQ, 2 Pods x 4 GPUs)
```

Draining `h200-08` evicted workers of `pcsg/llm-serve-1-decode/1`. Grove gang-terminated and recreated that replica, and its three replacement Pods are awaiting placement. The example scheduler events below report the reason.

#### `status`

```text
kubectl grove status [REF] [--only-unhealthy] [--pods] [--depth N] [-A]
```

Rolls Pod health up the hierarchy. Each row is `Degraded` unless:

| Row | `Ready` if | `Unavailable` if |
|---|---|---|
| PCLQ | all desired Pods ready | ready Pods < `minAvailable` |
| PCS or PCSG replica | all children `Ready` | any child `Unavailable` |
| PCSG | all replicas `Ready` | `Ready` + `Degraded` replicas < `minAvailable` |
| PCS | all replicas `Ready` | all replicas `Unavailable` |

Each row shows its worst descendant reason and how many Pods have it. A Pod's reason comes from its containers, else its `PodScheduled` condition, else `Pending` or `NotReady`. Worst first: `PodCreateFailed`, crash and image errors, `Unschedulable`, pending (including `SchedulingGated`), `NotReady`.

A PCLQ whose Pods the API server rejects (for example, an invalid environment variable name) has no Pods to report. While Pods are missing and `status.lastErrors` has `ERR_CREATE_POD`, it gets `PodCreateFailed` for each missing Pod, so `status` blames it, not its gated siblings.

READY is ready Pods for a PCLQ and Grove's `availableReplicas` for a PCS or PCSG, so a `Degraded` PCS can read `2/2`. GPUS is allocated/requested.

Pods appear only under unhealthy PCLQs unless `--pods` is set. `--only-unhealthy` and `--depth N` (replica rows count as levels) count what they hide in `omitted`. Pretty output collapses levels with a single replica; JSON keeps every replica record.

JSON: `namespace`; `groups[]` (`ref`, `parent`, `readyReplicas`, `replicas`, `gpusAllocated`, `gpusRequested`, `status`, `reason`, `count`); `pods[]` (`ref`, `parent`, `node`, `ready`, `gpusAllocated`, `gpusRequested`, `gang` from `grove.io/podgang`, `reason`); `reasons[]` (`reason`, `count`, `message`); `omitted` (`healthyGroups`, `healthyPods`, `belowDepthGroups`, `belowDepthPods`). With `-A`, `namespace` is `null` and each record carries its own.

```text
$ kubectl grove status pcs/llm-serve -n inference --only-unhealthy
NAME                                              KIND  READY  GPUS   STATUS                               NODE
llm-serve                                         PCS   2/2    36/48  Degraded: Unschedulable (3 pods)
└─ [1]                                                         12/24  Degraded: Unschedulable (3 pods)
   └─ decode                                      PCSG  1/2    12/24  Degraded: Unschedulable (3 pods)
      └─ [1]                                                   0/12   Unavailable: Unschedulable (3 pods)
         ├─ leader                                PCLQ  0/1    0/4    Unavailable: Unschedulable (1 pod)
         │  └─ llm-serve-1-decode-1-leader-x7k2p  Pod          0/4    Unschedulable                        <none>
         └─ worker                                PCLQ  0/2    0/8    Unavailable: Unschedulable (2 pods)
            ├─ llm-serve-1-decode-1-worker-m4q8z  Pod          0/4    Unschedulable                        <none>
            └─ llm-serve-1-decode-1-worker-w9d4f  Pod          0/4    Unschedulable                        <none>

Reasons:
  Unschedulable (3 pods): pod group needs 12 nvidia.com/gpu within one rack; max free in any rack: 8
13 healthy groups and 11 healthy pods hidden
```

#### `events`

```text
kubectl grove events [REF] [--all] [--no-group] [--since DURATION] [--reverse] [-A]
```

Shows the events of a subtree's PCS, PCSG, PCLQ, PodGang and Pod objects, grouped. A single PodGang or Pod is also accepted. For a PCS, select PodGangs whose controller owner UID matches the PCS UID. For a PCS replica, also match the `grove.io/podcliqueset-replica-index` label. Both queries find a gang even if Pod creation failed. For a PCSG, PCSG replica or PCLQ, use `grove.io/podgang` on Pods in the selected subtree to find associated gangs. PodGangMaps are excluded.

- **Matching:** by UID, with two fallbacks for replaced objects, whose events often explain the replacement (flagged `objectDeleted: true`):
  - A deleted Pod named `<pclq>-` plus a generated suffix goes to that subtree PCLQ.
  - A live Grove object with the same kind and name but a new UID (gang termination recreates PCLQs) gets the earlier object's events.
- **Scope:** ancestors are excluded, though Grove records some child events there; the output says so (`ancestorsExcluded: true`).
- **Filtering:** `Warning` only; `--all` adds `Normal`, which is otherwise counted in `omitted.normalEvents`. `--since` filters on when an event was last seen.
- **Grouping:** by type, reason, source and message, within a namespace. Generated Pod names in messages are masked first (`<podclique>-*`), and the `(combined from similar events): ` prefix is stripped. Each group has a summed `count`, `firstSeen`, `lastSeen`, `objects` (`{ref, count}`) and `scope`, the lowest common Grove ancestor. `--no-group` prints one unmasked row per object.
- **Order:** newest first; `--reverse` puts the oldest first.
- **Pretty output:** LAST SEEN, FIRST SEEN, TYPE, REASON, SOURCE, COUNT, SCOPE, with the message below and a retention notice last.
- **Data:** one `events.k8s.io/v1` list per namespace, matched in memory.

```text
$ kubectl grove events pcs/llm-serve -n inference
LAST SEEN  FIRST SEEN  TYPE     REASON                    SOURCE                COUNT  SCOPE
12s        41m         Warning  Unschedulable             kai-scheduler         27     pcsg/llm-serve-1-decode/1 (3 pods)
           pod group needs 12 nvidia.com/gpu within one rack; max free in any rack: 8
42m        47m         Warning  Unschedulable             kai-scheduler         12     pclq/llm-serve-1-decode-1-worker (2 deleted pods)
           pod group needs 8 nvidia.com/gpu in rack rack-a2; free in rack: 0
47m        47m         Warning  AllScheduledReplicasLost  podclique-controller  1      pclq/llm-serve-1-decode-1-worker (earlier incarnation)
           All scheduled pods lost (was 2). Gang termination will fire after TerminationDelay if the PodClique stays below MinAvailable; investigate node availability or capacity.
41 Normal events hidden (--all).
Events are retained only for the cluster's event TTL (default 1h); absence of events does not mean nothing happened.
```

#### `topology`

```text
kubectl grove topology [LEVEL] --for REF [-t BINDING] [--in LEVEL=VALUE,...]
```

Select a PCS, PCSG, PCLQ or replica with `--for`. Resolve its PodCliques in the target namespace. List Pods using the selected `grove.io/podclique` label values, check their controller owner UIDs against those PodCliques, and skip terminal Pods. Get each distinct Node named by the remaining Pods and read its labels for the binding’s levels.

Use the workload’s effective ClusterTopologyBinding by default. Use `-t/--topology` to inspect placement under another binding, with a warning if it differs from the workload’s. If the workload specifies none, use the only binding, ask for `-t` if several exist, or show placement by node if none exists.

At the requested level, each row gives the PodClique, the domain and the count of assigned Pods. JSON includes the broader domain path so identically named racks in different blocks stay distinct. With no level, show placement by node with its full domain path. A missing level label appears as `(unlabeled)` (`null` in JSON). List Pods without a node by name and parent PodClique under **Awaiting placement**. The scheduler’s explanation, when available, appears in `events`.

```text
$ kubectl grove topology rack --for pcsg/llm-serve-1-decode -n inference
PODCLIQUE                    ZONE    BLOCK    RACK     PODS
llm-serve-1-decode-0-leader  zone-1  block-a  rack-a1  1
llm-serve-1-decode-0-worker  zone-1  block-a  rack-a1  2

Awaiting placement
llm-serve-1-decode-1-leader  llm-serve-1-decode-1-leader-x7k2p
llm-serve-1-decode-1-worker  llm-serve-1-decode-1-worker-m4q8z, llm-serve-1-decode-1-worker-w9d4f
```

```json
{
  "namespace": "inference",
  "ref": "pcsg/llm-serve-1-decode",
  "topology": "h200-cluster",
  "level": "rack",
  "rows": [
    {"ref": "pclq/llm-serve-1-decode-0-leader", "path": {"zone": "zone-1", "block": "block-a"}, "value": "rack-a1", "pods": 1},
    {"ref": "pclq/llm-serve-1-decode-0-worker", "path": {"zone": "zone-1", "block": "block-a"}, "value": "rack-a1", "pods": 2}
  ],
  "awaitingPlacement": [
    {"ref": "pod/llm-serve-1-decode-1-leader-x7k2p", "parent": "pclq/llm-serve-1-decode-1-leader"},
    {"ref": "pod/llm-serve-1-decode-1-worker-m4q8z", "parent": "pclq/llm-serve-1-decode-1-worker"},
    {"ref": "pod/llm-serve-1-decode-1-worker-w9d4f", "parent": "pclq/llm-serve-1-decode-1-worker"}
  ]
}
```

`--in LEVEL=VALUE,...` filters placement rows to paths matching all the supplied broader levels. The list of Pods awaiting placement still covers the selected workload because those Pods have no domain yet.

#### `version`

```text
kubectl grove version [--grove-namespace NS]
```

Prints the client and operator versions and the operator's source link. The operator version comes from:

- Pods labeled `app.kubernetes.io/name=grove-operator`, in `--grove-namespace` or in all namespaces;
- their image tag, ignoring any digest, or, for a digest-only image, the `chart: grove-charts-<version>` label;
- every version present during a rollout, with a warning.

CI uses one `PACKAGE_VERSION` for image, binary and chart, so the tag identifies the release; `-dev` and custom tags are `unknown`.

- **Skew:** the same major.minor is compatible. Otherwise a stderr warning names the release to download, and the exit code stays 0. Only `version` checks.
- **Source link:** releases, including pre-releases such as `v0.1.0-alpha.17`, link to `https://github.com/ai-dynamo/grove/archive/refs/tags/<tag>.tar.gz`; `v0.0.0-g<sha>` links to `https://github.com/ai-dynamo/grove/archive/<sha>.tar.gz`; anything else is `unknown`, never `main`. `kubectl-grove` makes no requests outside the cluster. Users who want to debug locally can download the source from this link; neither the CLI nor the skills download it.

```text
$ kubectl grove version
kubectl grove   v0.6.0
Grove operator  v0.6.0  (pod grove-system/grove-operator-6c9f7b8d5-q4lzt)
Grove source    https://github.com/ai-dynamo/grove/archive/refs/tags/v0.6.0.tar.gz
                git clone --depth 1 --branch v0.6.0 https://github.com/ai-dynamo/grove
```

### GPU Accounting

`status` reports whole GPUs allocated and requested by the selected workload and warns about skipped devices. Kubernetes has two ways to request GPUs, and a cluster can use both:

- **Device plugin** (`nvidia.com/gpu`), the original and still most common: each Node advertises a GPU count that Pods request like CPU. `kubectl-grove` counts a Pod's effective request as the scheduler computes it (init containers, sidecars, Pod overhead).
- **DRA** (`gpu.nvidia.com`), newer and GA since Kubernetes 1.34: drivers publish individual GPUs in ResourceSlices, and Pods claim them through ResourceClaims. `status` reads requests from the workload’s claims or their templates and allocations from claims. A claim shared by several Pods ([GREP-390](../390-hierarchical-resource-sharing/README.md)) counts once. DRA is read from `resource.k8s.io/v1` only; if that API is missing or unreadable, only device-plugin GPUs are counted.

Workload totals include GPUs requested through either mechanism.

### Agent Skills

Ask your agent to install the skills with the following instruction, including after a CLI upgrade.

> Run `kubectl grove version`. From the `ai-dynamo/grove` release tag matching the CLI version, install `grove-debug`, `grove-topology` and `grove-diagnostics` from `.agents/skills/`. Write that tag to a `GROVE_RELEASE` file in each installed skill folder.

Before it starts, each skill compares its `GROVE_RELEASE` with the CLI version. If the file is missing or the major.minor versions differ, it repeats the instruction above and continues, noting the mismatch.

Each skill carries a generated copy of the shared reference. Its source is `tools/kubectl-grove/skills/reference/`; `make generate` refreshes the copies, and `make check` fails if they are stale.

The shared reference covers:

- the commands, a JSON field glossary, Grove's hierarchy and labels, and kubectl recipes;
- conditions to wait on: PCLQ `PodCliqueScheduled`, `MinAvailableBreached`, `UpdateInProgress`; PCSG `MinAvailableBreached`, `UpdateInProgress`, `GangTerminationInProgress`; PCS `UpdateInProgress`, `TopologyLevelsUnavailable`, `PodGangMigrationInProgress` (a PCS has no `MinAvailableBreached`); PodGang `Initialized`, `Scheduled`, `Ready`; CTB `SchedulerTopologyDrift`. `UpdateInProgress=Unknown` means the update passed its progress deadline;
- events Grove records on a parent about a child, and CTB events, which land in namespace `default`;
- what read-only access to one namespace cannot see (operator logs, Nodes, ClusterTopologyBindings and so `topology`), so the agent reports the gap instead of failing.

It also explains rollout progress (`grove.io/pod-template-hash`, or the generation hashes of [GREP-393](../393-coherent-rolling-updates/README.md)), inherited topology constraints, observed placement, and reading the operator's logs.

**`grove-debug`:** stuck or unhealthy workloads.

1. Preflight: `kubectl grove version` (CLI present, skills release, operator version if readable) and the target. The CLI reports a missing Grove installation or permission, so the skill reads no CRDs. With namespace-only access the operator shows as `unavailable`, as expected.
2. `status REF --only-unhealthy`: the deepest unhealthy groups are the blockers.
3. `events BLOCKER`.
4. Dig in (logs, PodGangs, constraints, hashes); hand placement to `grove-topology`.
5. Report; if asked to wait, `kubectl wait`, rerunning `status` on timeout.

**`grove-topology`** inspects binding health and workload placement.

1. Look up the workload’s binding and its levels with `kubectl get ctb`. Check the CTB’s `SchedulerTopologyDrift` and the PCS’s `TopologyLevelsUnavailable` conditions.
2. Run `kubectl grove topology LEVEL --for REF`. Compare each PodClique’s observed domains with the replica’s placement constraints.
3. For Pods awaiting placement, consult `status` and `events` for the affected replica.

**`grove-diagnostics`:** collect or analyze a bundle.

1. Scope: namespaces and, optionally, a PCS.
2. Capture resources, operator logs, Pods, events, `kubectl grove` JSON, versions and `manifest.json`; no Secrets or ConfigMaps; a `jq` filter strips environment values.
3. Package a timestamped local `.tgz`. Show the files listed in `manifest.json` and prompt the operator to inspect them locally before sharing, especially logs, events and workload arguments.
4. Offline analysis can proceed directly from the bundle.

### RBAC

`kubectl-grove` uses the caller's own credentials and installs no ServiceAccount or Role. The table lists the minimum permissions each command needs, so a cluster admin can grant exactly these to a restricted user or an agent's read-only credentials. Reads use `get` or `list` as indicated by the command’s access pattern. A missing permission fails the command with an error naming it, except for reads marked optional, which only warn ([Commands](#commands)).

| Command | Reads | Scope |
|---|---|---|
| `status` | PCS, PCSG, PCLQ, Pods; optional: ResourceClaims, ResourceClaimTemplates | Namespace, or all with `-A` |
| `events` | PCS, PCSG, PCLQ, Pods, PodGangs, `events.k8s.io` events | Namespace, or all with `-A` |
| `topology` | PCS, PCSG, PCLQ and Pods for the selected workload | Target namespace |
| `topology` | ClusterTopologyBindings (get/list), assigned Nodes (get by name) | Cluster |
| `version` | Optional: Pods | Operator namespace with `--grove-namespace`, else cluster |

### Backward Compatibility

- **Grove:** no API, operator or chart changes.
- **JSON:** may change in any major or minor release, not in patches; `--schema` always describes the installed binary's output.
- **Skills:** use the Grove release tag that matches the installed CLI. The CLI/operator compatibility rule is described under [version](#version).

### Monitoring

`kubectl-grove` is a client and exposes no metrics or endpoints. Errors and warnings go to stderr, and API server audit logs show its User-Agent, `kubectl-grove/<version>`.

### Dependencies

- **Go:** cli-runtime, client-go, Grove's clients, cobra, a JSON Schema generator.
- **Cluster:** Grove and `events.k8s.io/v1`. Workload GPU reporting with DRA uses `resource.k8s.io/v1` (Kubernetes 1.34+) and the NVIDIA DRA driver. ClusterTopologyBindings are optional.
- **Distribution:** Krew plugin `grove` (latest only); Linux and macOS binaries on every Grove release, versioned with the operator. The three skills ship in the same release’s source tree.
- **Location:** a Go module in `tools/kubectl-grove/` ([Open Questions](#open-questions)).

### Test Plan

**V1 acceptance requires both runs of the stuck-rollout scenario in Story 1 to pass.** A shared setup script builds the fixture in Grove's real-node e2e job, since KWOK nodes cannot pull images:

- **Workload:** `llm-serve` in the shape of Story 1 (two replicas, each with `frontend` and a `decode` scaling group of `leader` and `worker`), requesting no GPUs, with `updateStrategy.type: RollingRecreate` set explicitly.
- **Trigger:** after recording the Pods' `grove.io/pod-template-hash`, a rollout that changes only the worker image to one that can never be pulled, such as `registry.invalid/grove-fixture/worker:missing`. The `.invalid` domain never resolves, so the pull fails quickly and the same way everywhere.
- **Ready when:** the PCS has `UpdateInProgress=True`, a worker container waits with `ErrImagePull` or `ImagePullBackOff`, and a Warning `Failed` event names the image.
- **Expected answer:** derived with kubectl, not `kubectl grove`, from the failing Pod's `grove.io/podclique` label and that PCLQ's replica-index labels. The failing replica is the nearest replica above the Pod (`pcsg/llm-serve-<i>-decode/<j>`). The fixture also records its PCS replica, the Pod and the image.
- **Healthy sibling:** the fixture checks that the other PCS replica is Ready and its Pods still carry the recorded hash, rather than assuming an update order.

Both runs use a kubeconfig granting read-only access to the workload namespace. Only fixture setup and cleanup use write access. Under the `Coherent` strategy the blocker is a new PodGang rather than a replica, so that case is left to skill acceptance.

1. **CLI, automated in CI.** Run `kubectl grove status pcs/llm-serve -n inference -o json`. Follow the failing Pod’s parent refs to the nearest replica ref and pass that replica ref to `kubectl grove events` with the same namespace and JSON output. Assert that the replica matches the fixture and that a Warning attributed to its worker Pod reports a failed pull for the expected image. Generated Pod names come from the fixture. Timestamps, retry counts and exact message wording may vary.
2. **Debug skill, run by hand before release.** Start a fresh agent session with `grove-debug`, the CLI and the restricted kubeconfig. Keep the fixture script and expected diagnosis out of the session. Give the agent only this prompt: *“The rollout of `pcs/llm-serve` is stuck in `inference`. Find out why and draft an update for the incident thread.”* The tester compares the draft with the fixture. It must be ready to paste into the incident thread, identify the failing replica, cite the event on the affected Pod and explain how the failed image pull blocks the rollout.

Use gaps exposed by these runs to decide what else v1 needs. For example, if the CLI finds the failed Pod but the engineer cannot trace it to a replica with namespace-only access, the workflow is incomplete.

**Additional coverage**

- **Unit:** fake clients, golden files, and JSON checked against each schema.
- **Integration** on envtest covers clusters without Grove or DRA.
- **PodGang events:** in an envtest fixture with Grove installed, create a PodGang with a Warning event and zero Pods. Its event must appear for its owning PCS and the matching replica, and be absent from a sibling replica’s results.
- **End-to-end** on Grove's KWOK-based e2e cluster, with GPUs added to its nodes: broken workloads (real scheduling failures, simulated Pod failures) and a user with namespace-only RBAC.
- **Placement:** check per-PodClique counts across domains, unlabeled Nodes and Pods awaiting placement. Include an unrelated workload and verify the command succeeds with workload-namespace access, binding reads and `get` on the assigned Nodes only.
- **Real workers:** image pull and crash loop failures against a real kubelet, which KWOK only simulates.
- **Skill acceptance**, scripted and run on every skill or CLI change (outside CI, as it needs model access): curated broken workloads, including a stuck `Coherent` rollout, and replica placement scenarios, graded against the known cause or observed placement. Scan bundles for a canary environment value and excluded resources. Check that the skill lists the files from the manifest and prompts for local review before sharing.
- **Scale:** a step in Grove's weekly Scale Test CI, on its KWOK scale cluster with GPU nodes and workloads added, records requests, latency and memory per command in the scale history.

### Graduation Criteria

`kubectl-grove` is a separate, opt-in CLI and set of skills, released with Grove, so there is no feature gate and the stages do not apply. The first release ships in two PRs: the CLI, then the skills, which depend on its flags and JSON.

#### Alpha

N/A.

#### Beta

N/A.

#### GA

N/A.

## Implementation History

- **2026-01:** Issue [#373](https://github.com/ai-dynamo/grove/issues/373); first proposal [#374](https://github.com/ai-dynamo/grove/pull/374).
- **2026-03–04:** TUI prototype; GREP [#508](https://github.com/ai-dynamo/grove/pull/508).
- **2026-10:** Revised to a CLI plus agent skills.
- **2026-11:** Renamed from Arborist to `kubectl-grove`, the name of its binary.

## Alternatives

| Alternative | Why not |
|---|---|
| Interactive terminal UI (and its k9s plugin) | Management is moving to agents; a UI would cost far more to maintain and add less value. |
| Richer Grove status instead | Worth doing, but subtree events and workload placement need cross-object joins. |
| An MCP server | CLI plus skills is simpler: people and CI use the CLI without agents, and skills add what agents need. An MCP server adds no benefit. |
| A combined health and events command, or a diagnostics collector | Skills compose `status`, `events` and kubectl for these workflows. |
| A cluster GPU capacity view (free GPUs per domain) | Domain totals cannot show whether a gang will schedule, yet operators would read them that way. |
| One `grove` skill | Broad trigger, large context. |
| TOON (token-saving JSON encoding) | Savings unvalidated; JSON Schema stays the contract; addable later. |
| `apiVersion`/`kind` envelope | Implies a stability promise the JSON doesn't make ([Backward Compatibility](#backward-compatibility)). |
| `-o agent`, `-o wide` | More formats; JSON already has every field. |
| Unbounded JSON, bounded tables | Formats would show different rows; limits keep agent output small. |
| JSON by default when piped | The same command would print differently in a terminal and a script. |
| cli-runtime printers | `JSONPrinter` requires `apiVersion`/`kind`, then just calls `MarshalIndent`; `PrintFlags` adds many formats to the contract. |
| Own kubeconfig handling; kubectl's factory; kong | Reimplements kubectl's flags; heavy dependency; flags mapped by hand. |
| HPA, ResourceClaim and Node events | They relate to the workload but sit outside the PCS–PCSG–PCLQ–Pod hierarchy: each adds reads and permissions, and a Node's events cover every workload on it. Grove-created claims are an [open question](#open-questions). |
| Finding PodGangs only through Pod labels | Misses gangs whose Pods were never created, which is when their events matter most. |
| Reusing [Topograph](https://github.com/NVIDIA/topograph) | It writes topology labels; `kubectl-grove` reads them. Its reusable code is internal and needs newer Go and Kubernetes. |
| Exact version match | Krew has only the latest, so most users would see warnings. |
| Operator-published build info | Needs operator changes. |
| Source download in the CLI or skills | The CLI makes no requests outside the cluster, and skills diagnose from cluster state. Users who want to debug locally download the source from the `version` link. |
| Bundle skills in the CLI | Installing from the matching Grove release tag keeps the skills aligned with the CLI and lets the agent handle local installation. |
| `npx skills` or an agent plugin marketplace | Third-party or tied to one agent; asking the agent to install from the release tag works with any agent. |
| Stamp the release into the skills, or ship them as a release tarball | Stamping changes the release process, and a tarball departs from installing `.agents/skills/` at the tag. Recording the tag at install needs neither. |
| Symlinked or hand-copied shared reference | Symlinks break when an agent copies or downloads the files; hand copies drift. |
| Run the skill acceptance test in CI | Needs model access and is nondeterministic. |
| Compare each command against a kubectl-only baseline | Many model runs and a baseline skill to maintain; the [v1 acceptance tests](#test-plan) show the on-call workflow works. |

## Open Questions

- **Scheduler objects.** Should `events` follow PodGangs to [backend](../375-scheduler-backend-framework/README.md) PodGroups and Kueue Workloads?
- **Grove-created ResourceClaims.** Include events of claims from [resource sharing](../390-hierarchical-resource-sharing/README.md)?
- **Topology narrowing.** Proposed: `--in LEVEL=VALUE,...`. Alternatives: all-positional, or a nested tree with `--depth`.
- **Module location.** Proposed: `tools/kubectl-grove/`. Use the empty `cli-plugin/` module instead, or remove it?

## Future Work

- `--watch`, or a Grove-aware `wait`.
- `topology --pods` to list the assigned Pods behind each placement count.
- More event sources; a `--reason` filter.
- A `schema` subcommand; resource-name completion.
- MIG, partitioned and shared GPUs.
