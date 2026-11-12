# GREP-373: `kubectl-grove` CLI and Agent Skills

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Finding why a rollout is stuck](#story-1-finding-why-a-rollout-is-stuck)
    - [Story 2: Explaining an unschedulable replica](#story-2-explaining-an-unschedulable-replica)
    - [Story 3: Checking capacity before a deployment](#story-3-checking-capacity-before-a-deployment)
    - [Story 4: Collecting diagnostics for a bug report](#story-4-collecting-diagnostics-for-a-bug-report)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [Cluster Access](#cluster-access)
  - [Output](#output)
  - [References](#references)
  - [Commands](#commands)
    - [<code>tree</code>](#tree)
    - [<code>events</code>](#events)
    - [<code>topology</code>](#topology)
    - [<code>version</code>](#version)
    - [<code>skills install</code>](#skills-install)
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

`kubectl-grove` is a read-only kubectl plugin (`kubectl grove`) plus agent skills for debugging Grove workloads and finding GPU capacity. The CLI adds only views kubectl lacks: a workload's hierarchy with Pod health rolled up, its events, and GPU capacity per topology domain. The skills teach coding agents to combine these with kubectl. It installs through Krew and needs no change to Grove.

## Motivation

A Grove workload spans five resource kinds, and its replicas exist only as name indices. With kubectl, users walk this by hand to answer:

- **Where is the workload unhealthy, and why?** Higher levels report availability, not causes. For example, with a typo in the image registry, PodCliques report `MinAvailableBreached=False`, nothing above flags it, and `ErrImagePull` shows only on Pods.
- **What went wrong across the workload?** `kubectl events --for` covers one object and matches by name, so it mixes a recreated object's events with the earlier object's; name prefixes are ambiguous (`foo` vs `foo-bar`).
- **Where are GPUs free, and where did my workload land?** With [topology-aware scheduling](../244-topology-aware-scheduling/README.md), 16 GPUs packed in one rack need a rack with 16 free, which means joining topology levels, nodes and every Pod's GPU requests.

Coding agents can run kubectl, but the data is large and they lack Grove knowledge. `kubectl grove` commands do the joins; the skills carry the knowledge.

### Goals

- Find a workload's deepest unhealthy part, and why, in one command.
- Show a workload's events, grouped and correctly attributed even when objects are recreated.
- Show GPU capacity per topology domain and where a workload landed, for device-plugin and [DRA](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/) GPUs.
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

`kubectl-grove` adds five read-only commands under `kubectl grove` and three agent skills. Terms:

- **PCS, PCSG, PCLQ, PG, CTB:** PodCliqueSet, PodCliqueScalingGroup, PodClique, PodGang, ClusterTopologyBinding (short names `pcs`, `pcsg`, `pclq`, `pg`, `ctb`).
- **Ref:** `kind/name` (`pcs/llm-serve`), usable with kubectl; a **replica ref** (`pcs/<name>/<i>`, `pcsg/<name>/<i>`) works only with `kubectl grove`.
- **Subtree:** a ref and everything beneath it.
- **Grove Pod:** a Pod controlled by a PodClique.
- **Binding, level, domain:** a ClusterTopologyBinding lists levels (zone, rack, …), each a node label; a domain is one level value (`rack-a2`).
- **Agent skill:** a `SKILL.md` folder that coding agents load when a task matches.

| Command | Answers | Why not kubectl |
|---|---|---|
| `tree [REF]` | What is unhealthy, and why? | Walks four kinds and virtual replicas; rolls up Pod reasons. |
| `events [REF]` | What went wrong? | Needs UID matching across a subtree. |
| `topology [LEVEL]` | Where are GPUs free? Where did a workload land? | Needs a cluster-wide join. |
| `version` | Do CLI and operator match? Where is the source? | Only the binary knows its version. |
| `skills install [DIR]` | Installs the matching skills. | — |

| Skill | Use it to | Stories |
|---|---|---|
| `grove-debug` | Diagnose pending, crashing or stuck workloads. | [1](#story-1-finding-why-a-rollout-is-stuck) |
| `grove-topology` | Check topology setup, free GPUs, fit and placement. | [2](#story-2-explaining-an-unschedulable-replica), [3](#story-3-checking-capacity-before-a-deployment) |
| `grove-diagnostics` | Collect or analyze a diagnostics bundle. | [4](#story-4-collecting-diagnostics-for-a-bug-report) |

Design rules:

- **Minimal CLI:** commands only for data too large or join-heavy for an agent; skills compose the rest.
- **kubectl-identical access:** same flags, kubeconfig, namespace and auth.
- **Output for people and programs:** tables by default; `-o json` with a JSON Schema per command.
- **Skills inform:** they supply Grove knowledge and suggested steps; the agent chooses what to run.

### User Stories

#### Story 1: Finding why a rollout is stuck

As an on-call SRE, I want my agent to explain a stuck rollout, so that I can fix it without learning Grove.

`tree` shows the new Pods in `ErrImagePull` while the PodClique only reports an update in progress; `events` names the image.

#### Story 2: Explaining an unschedulable replica

As a platform operator, I want to know why a replica is pending despite free GPUs, so that I can tell shortage from fragmentation.

[`topology`](#topology) shows 24 free GPUs but at most 8 per rack, while the replica needs 12 in one rack: fragmentation. A cordoned node in `rack-a2` holds 8 idle GPUs.

#### Story 3: Checking capacity before a deployment

As a platform engineer, I want to find a cluster with room in one rack, so that my workload starts promptly.

An agent, or a CI `jq` check written against the published schema, compares each cluster's `topology rack -o json` to pick one or fail fast.

#### Story 4: Collecting diagnostics for a bug report

As a cluster operator, I want a secret-free diagnostics bundle, so that I can file a bug or let my agent analyze it offline.

`grove-diagnostics` strips environment values; for offline analysis it downloads the operator's exact source, after asking.

### Limitations/Risks & Mitigations

- **`topology` reads the whole cluster.** *Mitigation:* users without cluster-wide read get an error, not partial numbers ([RBAC](#rbac)); it is the costliest command, and scale [tests](#test-plan) decide on pagination.
- **Events expire; nothing is live.** *Mitigation:* `events` notes retention; skills add Pod status, conditions and logs, and rerun `tree` when `kubectl wait` times out.
- **Agents pay per token and vary between runs.** *Mitigation:* bounded [output](#output); skills narrow scope and filter with `jq`; repeated [test runs](#test-plan) measure accuracy.
- **Cluster text is untrusted.** *Mitigation:* `kubectl-grove` never acts on it; skills treat it as data, never as commands; docs recommend read-only credentials for agents.
- **Bundles may hold sensitive data.** *Mitigation:* no Secrets or ConfigMaps; a fixed `jq` filter strips environment values (canary-tested); bundles stay local and list their contents.
- **GPU accounting is partial** ([GPU Accounting](#gpu-accounting)); taints are ignored. *Mitigation:* warnings for uncounted devices; `grove-topology` checks taints when Pods pend despite free GPUs.
- **Version skew.** *Mitigation:* see [`version`](#version). Each skill records the CLI version it was installed with: on a skills/CLI mismatch it asks for `skills install`; on a CLI/operator mismatch it names the matching CLI release, continues and notes it.

## Design Details

### Cluster Access

The CLI is one binary, `kubectl-grove`, on `$PATH`. Each command lists each resource type once per namespace, answers in memory and exits; there are no informers and no state between runs.

[`k8s.io/cli-runtime`](https://github.com/kubernetes/cli-runtime) gives it kubectl's flags, kubeconfig loading, namespace defaulting, authentication, discovery and name resolution. Per-command `--context` lets agents target a cluster without changing the user's current context. Requests send the User-Agent `kubectl-grove/<version>` with raised client-side QPS. Commands use cobra.

### Output

Every command except `skills install` takes `-o pretty` (default; color on a terminal) or `-o json`; only the flag chooses. JSON rules:

- **Refs:** every record that names an object has a `ref`; a namespace shared by all records appears once at the top.
- **Flat:** records point to their `parent`; each record type has its own array (`groups[]`, `pods[]`).
- **Same rows:** both formats hide the same things and count them in `omitted`.
- **Deduplicated:** `tree` lists each (reason, message) once in `reasons[]`; `events` groups identical events.
- **Predictable:** stable order, integer counts, errors on stderr, no ANSI in JSON.
- **Complete:** JSON has every field; pretty output shows a subset plus notices, so there is no `-o wide`.

Part of the [`tree`](#tree) example as JSON (one branch, a few fields):

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

No ref means the whole namespace, or all namespaces with `-A`; a ref plus `-A` is an error. A PodGang belongs to a subtree when a Pod in the subtree names it in its `grove.io/podgang` label.

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

Draining `h200-08` evicted workers of `pcsg/llm-serve-1-decode/1`. Grove gang-terminated and recreated that replica, and its three new Pods (12 GPUs) fit in no rack.

#### `tree`

```text
kubectl grove tree [REF] [--only-unhealthy] [--pods] [--depth N] [-A]
```

Rolls Pod health up the hierarchy. Each row is `Degraded` unless:

| Row | `Ready` if | `Unavailable` if |
|---|---|---|
| PCLQ | all desired Pods ready | ready Pods < `minAvailable` |
| PCS or PCSG replica | all children `Ready` | any child `Unavailable` |
| PCSG | all replicas `Ready` | `Ready` + `Degraded` replicas < `minAvailable` |
| PCS | all replicas `Ready` | all replicas `Unavailable` |

Each row shows its worst descendant reason and how many Pods have it. A Pod's reason comes from its containers, else its `PodScheduled` condition, else `Pending` or `NotReady`. Worst first: `PodCreateFailed`, crash and image errors, `Unschedulable`, pending (including `SchedulingGated`), `NotReady`.

A PCLQ whose Pods the API server rejects (for example, an invalid environment variable name) has no Pods to report. While Pods are missing and `status.lastErrors` has `ERR_CREATE_POD`, it gets `PodCreateFailed` for each missing Pod, so `tree` blames it, not its gated siblings.

READY is ready Pods for a PCLQ and Grove's `availableReplicas` for a PCS or PCSG, so a `Degraded` PCS can read `2/2`. GPUS is allocated/requested.

Pods appear only under unhealthy PCLQs unless `--pods` is set. `--only-unhealthy` and `--depth N` (replica rows count as levels) count what they hide in `omitted`. Pretty output collapses levels with a single replica; JSON keeps every replica record.

JSON: `namespace`; `groups[]` (`ref`, `parent`, `readyReplicas`, `replicas`, `gpusAllocated`, `gpusRequested`, `status`, `reason`, `count`); `pods[]` (`ref`, `parent`, `node`, `ready`, `gpusAllocated`, `gpusRequested`, `gang` from `grove.io/podgang`, `reason`); `reasons[]` (`reason`, `count`, `message`); `omitted` (`healthyGroups`, `healthyPods`, `belowDepthGroups`, `belowDepthPods`). With `-A`, `namespace` is `null` and each record carries its own.

```text
$ kubectl grove tree pcs/llm-serve -n inference --only-unhealthy
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

Shows the events of a subtree's PCS, PCSG, PCLQ, PodGang and Pod objects, grouped. A single PodGang or Pod is also accepted.

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
kubectl grove topology [LEVEL] [-t BINDING] [--in LEVEL=VALUE,...] [--for REF]
```

Shows GPUs per domain of one level of a [ClusterTopologyBinding](../244-topology-aware-scheduling/README.md).

- **Binding:** `-t/--topology`, or the only one; several without `-t` is an error. With none, rows are nodes.
- **Overview** (no level): each level's label key and number of values (0 means a wrong key), and totals per GPU type.
- **Rows:** one per domain and GPU type (`nvidia.com/gpu.product`, else `unknown`), excluding terminal Pods. Nodes without the level's label form a `(none)` row, so rows add up to the totals.

| Column | GPUs |
|---|---|
| Capacity | allocatable, plus DRA ResourceSlices |
| Grove | requested by Grove Pods |
| Other | requested by other Pods |
| Free | Capacity − Grove − Other − Unavailable, at least 0 |
| Unavailable | unallocated on cordoned or NotReady nodes |

- **JSON:** each row has `path`, the values of its broader levels; lowest-level rows add `nodeState` and the Node's `ref`.
- **Footer:** unscheduled GPU-requesting Pods and their GPUs, Grove vs other, across GPU types and ignoring `--in`. Pods held by a scheduling gate count only in `gatedPods`.

```text
$ kubectl grove topology rack -t h200-cluster
RACK     TYPE  CAPACITY  GROVE  OTHER  FREE  UNAVAIL
rack-a1  H200  32        24     4      4     0        [▓▓▓▓▓▓▓▓▓▓▓▓░░  ]
rack-a2  H200  32        12     8      4     8        [▓▓▓▓▓▓░░░░  ××××]
rack-b1  H200  32        16     8      8     0        [▓▓▓▓▓▓▓▓░░░░    ]
rack-b2  H200  32        0      24     8     0        [░░░░░░░░░░░░    ]
Unscheduled: 3 pods requesting 12 GPUs (grove 12, other 0); 0 scheduling-gated pods
```

Bars: `▓` Grove, `░` other, blank free, `×` unavailable.

```json
{
  "topology": "h200-cluster",
  "level": "rack",
  "rows": [
    {"value": "rack-a2", "path": {"zone": "zone-1", "block": "block-a"}, "gpuType": "H200", "capacity": 32, "grove": 12, "other": 8, "free": 4, "unavailable": 8}
  ],
  "unscheduled": {"pods": 3, "gpusRequested": 12, "grove": 12, "other": 0, "gatedPods": 0}
}
```

- **`--in LEVEL=VALUE,...`** keeps nodes matching every pair. Levels must be broader than the one shown but need not be adjacent; an unknown level or value is an error listing the valid ones.
- **`--for REF`** (one PCS, PCSG or PCLQ ref, replica refs included) adds a column with that subtree's GPUs (`selectedGPUs`, a subset of Grove) and scopes the footer; it warns if the workload uses another binding. Check packing with a scaling-group replica ref (`pcsg/llm-serve-1-decode/0`).

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
- **Source link:** releases, including pre-releases such as `v0.1.0-alpha.17`, link to `https://github.com/ai-dynamo/grove/archive/refs/tags/<tag>.tar.gz`; `v0.0.0-g<sha>` links to `https://github.com/ai-dynamo/grove/archive/<sha>.tar.gz`; anything else is `unknown`, never `main`. `kubectl-grove` makes no requests outside the cluster.

```text
$ kubectl grove version
kubectl grove   v0.6.0
Grove operator  v0.6.0  (pod grove-system/grove-operator-6c9f7b8d5-q4lzt)
Grove source    https://github.com/ai-dynamo/grove/archive/refs/tags/v0.6.0.tar.gz
                git clone --depth 1 --branch v0.6.0 https://github.com/ai-dynamo/grove
```

#### `skills install`

```text
kubectl grove skills install [DIR]
```

Writes the skills embedded in the binary to `DIR`. Without `DIR`, a terminal user picks `~/.claude/skills/`, `~/.codex/skills/`, `~/.agents/skills/` or another path; without a terminal, it fails and lists them. It replaces only `grove-*` folders. It has no flags, no `-o json` and no other subcommands.

### GPU Accounting

`tree` and `topology` count whole GPUs only; other devices are skipped with a warning. Kubernetes has two ways to request GPUs, and a cluster can use both:

- **Device plugin** (`nvidia.com/gpu`), the original and still most common: each Node advertises a GPU count that Pods request like CPU. `kubectl-grove` counts a Pod's effective request as the scheduler computes it (init containers, sidecars, Pod overhead).
- **DRA** (`gpu.nvidia.com`), newer and GA since Kubernetes 1.34: drivers publish individual GPUs in ResourceSlices, and Pods claim them through ResourceClaims. `kubectl-grove` counts capacity from ResourceSlices, requests from claims or their templates, and allocations from claims. A claim shared by several Pods ([GREP-390](../390-hierarchical-resource-sharing/README.md)) counts once. DRA is read from `resource.k8s.io/v1` only; if that API is missing or unreadable, only device-plugin GPUs are counted.

Nodes using both are summed.

### Agent Skills

The skills are `grove-*` folders under `tools/kubectl-grove/skills/`, embedded in the binary. Each carries a copy of the shared reference (`tools/kubectl-grove/skills/_shared/`) and checks its version first ([Risks](#limitationsrisks--mitigations)); detailed content comes with the implementation.

The shared reference covers:

- the commands, a JSON field glossary, Grove's hierarchy and labels, and kubectl recipes;
- conditions to wait on: PCLQ `PodCliqueScheduled`, `MinAvailableBreached`, `UpdateInProgress`; PCSG `MinAvailableBreached`, `UpdateInProgress`, `GangTerminationInProgress`; PCS `UpdateInProgress`, `TopologyLevelsUnavailable`, `PodGangMigrationInProgress` (a PCS has no `MinAvailableBreached`); PodGang `Initialized`, `Scheduled`, `Ready`; CTB `SchedulerTopologyDrift`. `UpdateInProgress=Unknown` means the update passed its progress deadline;
- events Grove records on a parent about a child, and CTB events, which land in namespace `default`.

It also explains rollout progress (`grove.io/pod-template-hash`, or the generation hashes of [GREP-393](../393-coherent-rolling-updates/README.md)), inherited topology constraints, fit (demand vs free GPUs per domain), and reading the operator's logs and source.

**`grove-debug`:** stuck or unhealthy workloads.

1. Preflight: CLI, CRDs, target.
2. `tree REF --only-unhealthy`: the deepest unhealthy groups are the blockers.
3. `events BLOCKER`.
4. Dig in (logs, PodGangs, constraints, hashes, source); hand placement to `grove-topology`.
5. Report; if asked to wait, `kubectl wait`, rerunning `tree` on timeout.

**`grove-topology`:** binding health, capacity, fit and placement.

1. Binding: `kubectl get ctb` and the PCS's `topologyName`.
2. Health: `topology -t`, CTB `inSync` and `SchedulerTopologyDrift`, PCS `TopologyLevelsUnavailable`.
3. Capacity: `--in`; free GPUs vs unscheduled demand; taints if free GPUs go unused.
4. Placement: `--for` with replica refs; report.

**`grove-diagnostics`:** collect or analyze a bundle.

1. Scope: namespaces and, optionally, a PCS.
2. Capture resources, operator logs, Pods, events, `kubectl grove` JSON, versions and `manifest.json`; no Secrets or ConfigMaps; a `jq` filter strips environment values.
3. Package a timestamped `.tgz`; report its contents.
4. Optionally analyze it with the matching source.

### RBAC

`kubectl-grove` uses the caller's own credentials and installs no ServiceAccount or Role. The table lists the minimum permissions each command needs, so a cluster admin can grant exactly these to a restricted user or an agent's read-only credentials. All are `get` and `list`. A missing permission fails the command with an error naming it, except for reads marked optional, which only warn ([Commands](#commands)). `skills install` reads nothing.

| Command | Reads | Scope |
|---|---|---|
| `tree` | PCS, PCSG, PCLQ, Pods; optional: ResourceClaims, ResourceClaimTemplates | Namespace, or all with `-A` |
| `events` | PCS, PCSG, PCLQ, Pods, PodGangs, `events.k8s.io` events | Namespace, or all with `-A` |
| `topology` | ClusterTopologyBindings, Nodes, Pods, the `--for` target; optional: ResourceSlices, ResourceClaims, ResourceClaimTemplates | Cluster |
| `version` | Optional: Pods | Operator namespace with `--grove-namespace`, else cluster |

### Backward Compatibility

- **Grove:** no API, operator or chart changes.
- **JSON:** may change in any major or minor release, not in patches; `--schema` always describes the installed binary's output.
- **Skills:** embedded in the binary; they check themselves against the CLI.

### Monitoring

`kubectl-grove` is a client and exposes no metrics or endpoints. Errors and warnings go to stderr, and API server audit logs show its User-Agent, `kubectl-grove/<version>`.

### Dependencies

- **Go:** cli-runtime, client-go, Grove's clients, cobra, a JSON Schema generator.
- **Cluster:** Grove; `events.k8s.io/v1`; for DRA, `resource.k8s.io/v1` (Kubernetes 1.34+) and the NVIDIA DRA driver; `nvidia.com/gpu.product` node labels. ClusterTopologyBindings are optional.
- **Distribution:** Krew plugin `grove` (latest only); Linux and macOS binaries on every Grove release, versioned with the operator.
- **Location:** a Go module in `tools/kubectl-grove/` ([Open Questions](#open-questions)).

### Test Plan

- **Unit:** fake clients, golden files, and JSON checked against each schema.
- **Integration** on envtest: a cluster without Grove, and one without DRA.
- **End-to-end** on Grove's KWOK-based e2e cluster, with GPUs added to its nodes: broken workloads (real scheduling failures, simulated Pod failures) and a user with namespace-only RBAC.
- **Real workers:** image pull and crash loop failures against a real kubelet, which KWOK only simulates.
- **Skill acceptance**, scripted and run on every skill or CLI change (outside CI, as it needs model access): curated broken workloads plus new `topology` scenarios, several runs each, graded against the documented root cause. Bundles are scanned for a canary environment value and Secret.
- **Scale:** a step in Grove's weekly Scale Test CI, on its KWOK scale cluster with GPU nodes and workloads added, records requests, latency and memory per command in the scale history.

### Graduation Criteria

`kubectl-grove` is a separate, opt-in CLI and set of skills, released with Grove, so there is no feature gate and the stages do not apply.

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
| Richer Grove status instead | Worth doing, but subtree events and GPU capacity need cross-object joins. |
| An MCP server | CLI plus skills is simpler: people and CI use the CLI without agents, and skills add what agents need. An MCP server adds no benefit. |
| `status` or `diagnostics` command | Compositions; skills describe them. |
| One `grove` skill | Broad trigger, large context. |
| TOON (token-saving JSON encoding) | Savings unvalidated; JSON Schema stays the contract; addable later. |
| `apiVersion`/`kind` envelope | Implies stability alpha can't promise. |
| `-o agent`, `-o wide` | More formats; JSON already has every field. |
| Unbounded JSON, bounded tables | Formats would show different rows; limits keep agent output small. |
| JSON by default when piped | The same command would print differently in a terminal and a script. |
| cli-runtime printers | `JSONPrinter` requires `apiVersion`/`kind`, then just calls `MarshalIndent`; `PrintFlags` adds many formats to the contract. |
| Own kubeconfig handling; kubectl's factory; kong | Reimplements kubectl's flags; heavy dependency; flags mapped by hand. |
| HPA, ResourceClaim and Node events | They relate to the workload but sit outside the PCS–PCSG–PCLQ–Pod hierarchy: each adds reads and permissions, and a Node's events cover every workload on it. Grove-created claims are an [open question](#open-questions). |
| Reusing [Topograph](https://github.com/NVIDIA/topograph) | It writes topology labels; `kubectl-grove` reads them. Its reusable code is internal and needs newer Go and Kubernetes. |
| Exact version match | Krew has only the latest, so most users would see warnings. |
| Operator-published build info | Needs operator changes. |
| Source download in the CLI | No requests outside the cluster. |
| Manual, `npx skills` or marketplace installs | Not version-matched; still work. |

## Open Questions

- **PodGang events.** Include PodGangs, which users don't author? [PodGangMaps](../393-coherent-rolling-updates/README.md) are excluded.
- **Scheduler objects.** Should `events` follow PodGangs to [backend](../375-scheduler-backend-framework/README.md) PodGroups and Kueue Workloads?
- **Grove-created ResourceClaims.** Include events of claims from [resource sharing](../390-hierarchical-resource-sharing/README.md)?
- **Topology narrowing.** Proposed: `--in LEVEL=VALUE,...`. Alternatives: all-positional, or a nested tree with `--depth`.
- **Module location.** Proposed: `tools/kubectl-grove/`. Use the empty `cli-plugin/` module instead, or remove it?

## Future Work

- `--watch`, or a Grove-aware `wait`.
- `topology --pods`: GPU-requesting Pods per row.
- More event sources; a `--reason` filter.
- A `schema` subcommand; resource-name completion.
- MIG, partitioned and shared GPUs.
