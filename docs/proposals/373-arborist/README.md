# GREP-373: Arborist — `kubectl grove` CLI and Agent Skills

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Terminology](#terminology)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Finding why a rollout is stuck](#story-1-finding-why-a-rollout-is-stuck)
    - [Story 2: Explaining an unschedulable replica](#story-2-explaining-an-unschedulable-replica)
    - [Story 3: Checking capacity before a deployment](#story-3-checking-capacity-before-a-deployment)
    - [Story 4: Collecting diagnostics for a bug report](#story-4-collecting-diagnostics-for-a-bug-report)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
    - [Cluster-wide read permissions](#cluster-wide-read-permissions)
    - [API server load on large clusters](#api-server-load-on-large-clusters)
    - [Events are incomplete by nature](#events-are-incomplete-by-nature)
    - [Large outputs cost agents tokens](#large-outputs-cost-agents-tokens)
    - [Agents are non-deterministic](#agents-are-non-deterministic)
    - [Untrusted text reaches the agent](#untrusted-text-reaches-the-agent)
    - [Version skew between CLI, skills and operator](#version-skew-between-cli-skills-and-operator)
    - [Sensitive data in diagnostics bundles](#sensitive-data-in-diagnostics-bundles)
    - [No real-time view](#no-real-time-view)
    - [Incomplete GPU accounting](#incomplete-gpu-accounting)
    - [JSON output changes during alpha](#json-output-changes-during-alpha)
- [Design Details](#design-details)
  - [Architecture](#architecture)
  - [Cluster Access](#cluster-access)
  - [Output Contract](#output-contract)
  - [Resource References](#resource-references)
  - [Commands](#commands)
    - [<code>tree</code>](#tree)
    - [<code>events</code>](#events)
    - [<code>topology</code>](#topology)
    - [<code>version</code>](#version)
    - [<code>skills install</code>](#skills-install)
  - [GPU Accounting](#gpu-accounting)
  - [Agent Skills](#agent-skills)
    - [Format and packaging](#format-and-packaging)
    - [What the skills know](#what-the-skills-know)
    - [<code>grove-debug</code>](#grove-debug)
    - [<code>grove-topology</code>](#grove-topology)
    - [<code>grove-diagnostics</code>](#grove-diagnostics)
  - [RBAC](#rbac)
  - [Distribution](#distribution)
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
  - [Interactive terminal UI](#interactive-terminal-ui)
  - [Improving Grove's status so kubectl is enough](#improving-groves-status-so-kubectl-is-enough)
  - [Other tool shapes](#other-tool-shapes)
  - [Output formats](#output-formats)
  - [Cluster access and output](#cluster-access-and-output)
  - [Events scope](#events-scope)
  - [<code>tree</code> roll-up](#tree-roll-up)
  - [Topology command](#topology-command)
  - [Version checking](#version-checking)
  - [Skill distribution](#skill-distribution)
- [Open Questions](#open-questions)
- [Future Work](#future-work)
- [Appendix](#appendix)
  - [POC error scenarios](#poc-error-scenarios)
  - [Version detection prior art](#version-detection-prior-art)
<!-- /toc -->

## Summary

Arborist is a read-only `kubectl grove` plugin and a set of agent skills that help operators debug Grove workloads and understand GPU capacity on their own clusters. The plugin adds only the views kubectl cannot produce without heavy cross-referencing: a workload's hierarchy with health rolled up from its Pods, all events of a workload or any part of it, and GPU capacity per topology domain, each as a table or as JSON with a published schema. The skills give coding agents the Grove knowledge they need to combine these views with kubectl to find root causes, explain placement and collect diagnostics. Arborist requires no change to Grove's APIs or operator and never changes cluster state.

## Motivation

A Grove workload spans several resource kinds: a PodCliqueSet owns PodCliques and PodCliqueScalingGroups, scaling groups own PodCliques, PodCliques own Pods, and the operator creates PodGangs for the scheduler. Replicas of PodCliqueSets and scaling groups are not objects of their own, so a "replica" exists only as a naming convention across several objects. Answering ordinary operational questions with kubectl means walking this hierarchy by hand:

- **Where is the workload unhealthy, and why?** Status on the higher levels summarises availability, not causes. In the POC scenario with a typo in the image registry, the PodCliques report `MinAvailableBreached=False` ("Either sufficient ready or starting pods found"), the scaling group reports no breach either, and the PodCliqueSet has no such condition, while the actual problem, `ErrImagePull`, is visible only on the Pods. Finding it means listing every level and every Pod.
- **What went wrong across the workload?** Events are recorded on the object they concern, so one workload's events are spread across PodCliqueSets, scaling groups, PodCliques, PodGangs and Pods. `kubectl events --for` covers a single object and matches it by name, so it cannot tell a recreated object's events from its predecessor's; matching a whole workload by name prefix is ambiguous (`foo` vs `foo-bar`) and misses Pods that have already been replaced.
- **Where are GPUs free, and where did my workload land?** With [topology-aware scheduling](../244-topology-aware-scheduling/README.md), a workload that needs 16 GPUs packed in one rack fits only where a single rack has 16 free GPUs. Answering this needs a cluster-wide join of a ClusterTopologyBinding's levels, node labels and GPU capacity, every Pod's GPU requests (Grove and non-Grove) and, with DRA, ResourceSlices and ResourceClaim allocations.

More and more operators debug with coding agents that can run kubectl themselves. Two things hold them back on Grove. First, the raw data is large: one workload can have hundreds of Pods and thousands of events, and an agent that lists and joins them spends many turns and tokens and is likely to miss things. Second, the agent lacks Grove-specific knowledge: which labels select a workload's Pods, which conditions to wait on, which events Grove records on a parent object but which describe a child, and how topology constraints are inherited. Arborist addresses both: a few commands that do the large joins and return small, structured answers, and skills that carry the Grove knowledge and teach the agent how to combine those answers with kubectl.

### Goals

- Let an operator, or their agent, find the deepest unhealthy part of a Grove workload and its reason with one command, without knowing Grove's resource hierarchy.
- Show the events of a Grove workload, or any part of it, grouped and deduplicated, attributed to the right object even after Pods and PodCliques have been recreated.
- Show GPU capacity per topology domain, split into Grove, other, free and unavailable GPUs, and show where a given workload or replica landed, for both device-plugin and DRA GPUs.
- Give every view a machine-readable form with a published JSON Schema, so agents and scripts consume structured data instead of scraping text.
- Give coding agents the Grove-specific knowledge to debug workloads, check placement and collect diagnostics with Arborist and kubectl, in a version that always matches the installed CLI.
- Report version skew between the CLI and the running Grove operator, and point to the exact source code of the running operator.

### Non-Goals

- **Re-implementing kubectl.** YAML retrieval, logs, exec, watching single objects and condition-based waiting stay in kubectl; the skills teach the agent the right kubectl invocations for Grove resources.
- **Changing cluster state.** No Arborist command writes to the cluster. Skills may recommend a kubectl command for the user to run; lifecycle commands (`rollout`, `scale`, `restart`) are out of scope.
- **An interactive UI or k9s plugin.** See [Alternatives](#interactive-terminal-ui).
- **Live views.** `--watch` and a Grove-aware `wait` are deferred to [Future Work](#future-work); blocking waits use `kubectl wait`.
- **Network access outside the cluster.** Arborist does not download source code or check for newer releases; it only prints a link to the source.
- **Changes to Grove's APIs, operator or Helm chart.** Everything Arborist reports is derived from existing objects.
- **Tooling for Grove contributors.** The audience is people operating Grove; for example, locally built operator images report their version as `unknown`.
- **Fractional GPU accounting.** MIG, partitioned and shared devices are not counted in this iteration; see [Future Work](#future-work).

## Terminology

| Term | Meaning |
|---|---|
| PCS, PCSG, PCLQ, PG, CTB | PodCliqueSet, PodCliqueScalingGroup, PodClique, PodGang, ClusterTopologyBinding; their kubectl short names are `pcs`, `pcsg`, `pclq`, `pg` and `ctb` |
| **Grove Pod** | A Pod whose controller owner is a PodClique. All other Pods are "other" Pods. |
| **ref** | A `kind/name` reference using kubectl short names (`pcs/llm-serve`, `pod/llm-serve-0-frontend-x7k2p`). Refs printed by Arborist can be passed to kubectl unchanged, except replica refs and refs of deleted objects. |
| **Replica ref** | `pcs/<name>/<i>` or `pcsg/<name>/<i>`: replica `i` of a PCS or PCSG. Replicas are not objects, so only Arborist understands replica refs. |
| **Subtree** | A ref and everything beneath it in the Grove hierarchy: Grove resources and Pods. |
| **Agent skill** | A folder containing a `SKILL.md` file (a name and description in front matter, then instructions) that coding agents load when a task matches the description. |
| DRA | Kubernetes [Dynamic Resource Allocation](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/) |

## Proposal

Arborist has two parts that are designed together: a minimal CLI and three agent skills.

**The CLI** (`kubectl grove`) has five commands:

| Command | Answers | Why kubectl alone is not enough |
|---|---|---|
| `tree [ref]` | Which part of the workload is unhealthy, and why? | Requires walking owner references across four kinds and virtual replica levels, then rolling Pod-level reasons up. Higher-level conditions can look healthy while Pods fail. |
| `events [ref]` | What went wrong anywhere in the workload? | `kubectl events --for` covers one object; a subtree needs matching events by UID against hundreds of objects. |
| `topology [level]` | Where are GPUs free or used, per topology domain? Where did a workload land? | Requires a cluster-wide join of topology levels, node labels and capacity, all Pods' GPU requests and DRA allocations. |
| `version` | Do the CLI and operator versions match? Where is the exact source of the running operator? | The CLI's version is known only to the binary. |
| `skills install [dir]` | Installs the skills that match this binary. | — |

**The skills** carry what an agent needs to know about Grove and when to use which tool:

| Skill | Use it to | User stories |
|---|---|---|
| `grove-debug` | Diagnose unhealthy or stuck workloads: pending, unschedulable, crashing, image errors, rollouts not completing. | [1](#story-1-finding-why-a-rollout-is-stuck) |
| `grove-topology` | Check topology setup and scheduler sync, find free GPU capacity and fragmentation, decide whether a workload fits, see where a workload or replica landed. | [2](#story-2-explaining-an-unschedulable-replica), [3](#story-3-checking-capacity-before-a-deployment) |
| `grove-diagnostics` | Collect a diagnostics bundle for a bug report, or analyse one offline. | [4](#story-4-collecting-diagnostics-for-a-bug-report) |

The design follows six rules:

1. **Don't duplicate kubectl.** A command exists only if it provides something kubectl cannot without manual cross-referencing: walking the Grove hierarchy, aggregating across descendants, GPU accounting, grouping by topology.
2. **Keep the CLI minimal; composition lives in the skills.** A command earns its place only when the raw data is too large or too join-heavy for an agent to process cheaply. Anything that works on small inputs (a single spec, or the JSON of an Arborist command) belongs in a skill. There is no `status` or `diagnostics` command: both are compositions that the skills describe.
3. **Read-only.** Arborist never writes to the cluster, so it needs only read RBAC.
4. **kubectl-identical cluster access.** Same flags, kubeconfig handling, namespace defaulting and authentication as kubectl.
5. **Output for people, agents and scripts.** A readable default, and `-o json` with a JSON Schema per command.
6. **Skills inform; they don't prescribe.** They surface tools and Grove semantics and let the agent decide how to proceed; remediation advice is minimal.

`tree`, `events` and `topology` must also prove their value before the first release: an agent using them is compared with an agent using only kubectl on the same broken workloads, and a command that shows no gain is dropped (see [Test Plan](#test-plan)).

### User Stories

#### Story 1: Finding why a rollout is stuck

As an operator on call for a GPU cluster, I want my coding agent to find why a PodCliqueSet rollout is not completing, so that I can fix it without first learning Grove's resource hierarchy.

The operator asks their agent why `rag-api` is not finishing its rollout, and the `grove-debug` skill is selected. The agent checks that `kubectl grove` is installed and compatible with the operator, then runs `kubectl grove tree pcs/rag-api --only-unhealthy -o json`. The deepest unhealthy groups are the blockers: the new Pods of the `retriever` PodClique report `ErrImagePull`, while the PodClique's conditions say only that an update is in progress (`UpdateInProgress=True`, and `MinAvailableBreached=Unknown` for the duration of the update). `kubectl grove events pclq/rag-api-0-retriever -o json` returns the kubelet's `Failed` events, one of which names the image that cannot be pulled, and comparing each Pod's `grove.io/pod-template-hash` label with its PodClique's confirms that only the new Pods are affected. The agent reports the root cause, the evidence and the affected objects, and suggests correcting the image registry in the PodCliqueSet spec. It runs nothing that changes the cluster.

#### Story 2: Explaining an unschedulable replica

As a platform operator, I want to know why one replica of a workload stays pending while the cluster still has free GPUs, so that I can tell a capacity shortage from fragmentation and fix the right thing.

`grove-debug` finds that decode replica 1 of `llm-serve` replica 1 has three `Unschedulable` Pods in one gang, and the events show that the scheduler cannot fit them into one rack. It hands over to `grove-topology`, which runs `kubectl grove topology rack -t h200-cluster --for pcs/llm-serve/1 -n inference`. The output shows 24 free GPUs cluster-wide but at most 8 in any rack, while the replica's unscheduled Pods request 12: fragmentation, not a shortage. Narrowing to the nodes of `rack-a2` shows a cordoned node holding 8 idle GPUs; uncordoning it would leave exactly the 12 GPUs the gang needs. The [`topology` examples](#topology) show this session.

#### Story 3: Checking capacity before a deployment

As a platform engineer running several GPU clusters, I want to know which cluster has a rack with enough free GPUs of the right type before I deploy a workload that must be packed into one rack, so that it starts instead of waiting in a cluster that cannot fit it.

The engineer asks their agent, which uses `grove-topology` to run `kubectl grove topology rack -o json` against each cluster's context and compare free GPUs per rack with the workload's demand. The same check runs without an agent in the team's deployment pipeline: a `jq` expression over the JSON picks a target cluster or fails fast. Because the JSON is described by a published schema (`kubectl grove topology --schema`), the check is written against documented fields, and when it fails the log shows the same Grove, other, free and unavailable numbers an operator would see, so contention from non-Grove workloads is visible.

#### Story 4: Collecting diagnostics for a bug report

As an operator who has hit unexpected behaviour, I want to collect a complete diagnostics bundle without leaking secrets, so that I can file a bug report or have my agent analyse it offline.

The operator asks their agent to collect diagnostics for `llm-serve`, and the `grove-diagnostics` skill is selected. The agent captures the Grove resources, operator logs and status, Pods, events, the JSON of `tree`, `events` and `topology`, and the versions of the CLI, operator and CRDs into a timestamped `.tgz` with a `manifest.json` describing each file. Secrets and ConfigMaps are not collected, and environment variable values are stripped. The agent reports what was and was not collected. The operator can attach the bundle to a GitHub issue or, if they prefer not to share it, ask the agent to analyse it locally against the Grove source for the operator's exact version.

### Limitations/Risks & Mitigations

#### Cluster-wide read permissions

`topology` counts the GPUs of every Pod on every node, so it needs cluster-wide `list` on Pods and Nodes. `version` lists Pods across all namespaces unless it is told the operator's namespace. Users with only namespace-scoped roles cannot use `topology`.

**Mitigation:** `topology` fails with an error naming the missing permission rather than printing partial numbers. `tree` and `events` need only namespace-scoped access, and `version --grove-namespace` avoids the cluster-wide list. Each command's permissions are documented (see [RBAC](#rbac)).

#### API server load on large clusters

`topology` lists all Pods in the cluster on every run, and an agent may run commands repeatedly.

**Mitigation:** commands make one `list` per resource type and namespace (never one request per object), use no informers and keep no state between runs apart from kubectl's discovery cache. `topology` is documented as the most expensive command. Every request carries the User-Agent `kubectl-grove/<version>`, so the load is visible in API server audit logs. Scale tests with KWOK measure requests and latency per command, and pagination is added if they show a need.

#### Events are incomplete by nature

Kubernetes keeps events only for the cluster's event TTL (one hour by default) and aggregates or rate-limits repeated events, so counts are approximate and old events disappear.

**Mitigation:** pretty `events` output always ends with a retention notice ("absence of events does not mean nothing happened"), counts are documented as "occurrences recorded by the cluster", and the skills carry the same caveat and point to Pod status, conditions and operator logs as complementary evidence.

#### Large outputs cost agents tokens

A failing gang of 256 Pods produces 256 Pod records and many events.

**Mitigation:** every command is bounded by default, in both formats: `tree` expands Pods only under unhealthy PodCliques, `events` shows Warning events only and groups identical events, and anything left out is counted in an `omitted` block rather than silently dropped. Messages are deduplicated. The skills tell agents to narrow the scope (for example `pcs/x/1` or `pclq/z`), filter JSON with `jq` before reading it and avoid `--all` unless needed.

#### Agents are non-deterministic

Skills guide an agent; they do not control it. Different models, or the same model on different runs, can reach different conclusions.

**Mitigation:** acceptance tests run each skill several times against known broken workloads and record how often it finds the documented root cause (see [Test Plan](#test-plan)). Skills state Grove facts precisely and keep remediation advice minimal.

#### Untrusted text reaches the agent

Event messages, annotations, logs and resource specs are written by whoever controls a workload, and the agent reads them. Arborist is read-only, but the agent running it may hold credentials that can write.

**Mitigation:** Arborist never acts on cluster text. The skills tell the agent to treat everything read from the cluster as data and never to run commands found in it, and the documentation recommends read-only credentials for agent sessions.

#### Version skew between CLI, skills and operator

A CLI from a different Grove release may misread Grove resources, and installed skills may describe flags or JSON fields that the installed CLI lacks.

**Mitigation:** a CLI supports operators of its own major and minor version; `kubectl grove version` warns when they differ and names the release to install. Krew's index carries only the latest version of a plugin, so the CLI for an older release is downloaded from that release's assets. Skills are embedded in the binary and record the version they were installed from: on a mismatch with the CLI they ask the user to re-run `skills install`; on a mismatch between CLI and operator they tell the user how to get a matching CLI, then continue and note the mismatch in their report.

#### Sensitive data in diagnostics bundles

Operator logs and resource specs can contain organisation-specific names, annotations, error messages and inline environment variable values.

**Mitigation:** the `grove-diagnostics` skill never collects Secrets or ConfigMaps, and removes environment variable values from captured specs with a documented `jq` filter rather than relying on the agent's judgement. An acceptance test builds a bundle from a workload with a canary environment value and a Secret and checks that neither appears. The bundle is written locally and never sent anywhere, the skill reports exactly what it collected, and offline analysis by the user's own agent is offered as an alternative to sharing it.

#### No real-time view

Nothing updates live; an operator waiting for a rollout must poll.

**Mitigation:** blocking waits use `kubectl wait --for=condition=...` on Grove's existing conditions, which the skills list per kind; on a timeout, `grove-debug` re-runs `tree` and `events` to explain why the condition is not met. `--watch` and a Grove-aware `wait` are candidates for [future work](#future-work).

#### Incomplete GPU accounting

Only whole GPUs are counted, and taints are ignored: GPUs on a tainted node count as free although only Pods that tolerate the taint can use them.

**Mitigation:** commands warn on stderr about devices they do not count. Cordoned and NotReady nodes are reported as unavailable rather than free. There is no mitigation for taints in this iteration beyond `grove-topology` telling the agent to check node taints when capacity looks free but Pods stay pending.

#### JSON output changes during alpha

Agents and scripts that parse the JSON may break when it changes.

**Mitigation:** each command's JSON Schema is generated from the Go types that produce the output and printed by `--schema`, so consumers can see the exact contract of the binary they run. Alpha makes no stability promise; a `schemaVersion` field and compatibility rules are introduced at Beta (see [Graduation Criteria](#graduation-criteria)).

## Design Details

### Architecture

Arborist is a single binary, `kubectl-grove`, which kubectl finds on `$PATH` and runs for `kubectl grove ...`. Each command lists what it needs once (one `list` per resource type and namespace), computes its answer in memory, prints it and exits: there are no informers and no state kept between runs. `tree` and `events` share the code that loads a namespace's Grove resources and Grove Pods; `tree` and `topology` share the GPU accounting.

### Cluster Access

kubectl does not pass its resolved configuration to plugins; each plugin loads it itself. Arborist uses `genericclioptions.ConfigFlags` from [`k8s.io/cli-runtime`](https://github.com/kubernetes/cli-runtime), the library kubectl is built on, so `kubectl grove` resolves the cluster, credentials and namespace exactly as kubectl does:

- **The same connection flags:** all of kubectl's, including `--kubeconfig`, `--context`, `--cluster`, `--user`, `-n/--namespace`, the impersonation flags (`--as` and related), `--token`, `-s/--server`, the TLS flags, `--request-timeout` and `--cache-dir`. Arborist's own flags do not reuse these names.
- **The same kubeconfig loading** (`--kubeconfig` alone if given, otherwise the files in `KUBECONFIG` merged, otherwise `~/.kube/config`; in-cluster configuration only when there is no kubeconfig) and **namespace defaulting** (`-n`, then the context's namespace, then, inside a Pod, the Pod's namespace, then `default`).
- **The same authentication** (exec credential plugins, tokens, client certificates) and **discovery cache** as kubectl. Discovery also answers whether Grove's CRDs are installed (otherwise Arborist reports that Grove is not installed) and whether the DRA API is served.
- **The same resolution of resource names**, so `pcs/x`, `podcliqueset/x` and `podcliquesets.grove.io/x` resolve from the CRDs' short names, with kubectl's errors for unknown types.

Every request carries the User-Agent `kubectl-grove/<version>`. Client-side QPS and burst for API requests are raised above client-go's defaults of 5 and 10, so the few list calls a command makes are not throttled on the client (discovery uses kubectl's own settings); the API server's priority and fairness limits still apply. Per-command `--context` and `-n` also matter for agents: an agent can target a cluster explicitly instead of running `kubectl config use-context`, which would change the user's shared current context.

Arborist builds its own clients for Grove's resources from the resolved configuration. It does not use cli-runtime's printers or kubectl's command factory (see [Alternatives](#cluster-access-and-output)). Commands are built with [cobra](https://github.com/spf13/cobra), like kubectl.

### Output Contract

Every command except `skills install` supports two formats: `-o pretty` (the default; tables, with colour and box drawing when stdout is a terminal) and `-o json`. Output never switches format by itself; skills and scripts pass `-o json` explicitly.

The JSON follows seven principles:

1. **Full refs.** Every record that names an object has a `ref`, and the namespace is hoisted to the top level when it is the same for all records, so a ref can be passed to kubectl or back to Arborist.
2. **Flat records with `parent` pointers** rather than deep nesting. Records of different shapes go in separate arrays (for example `groups[]` and `pods[]`).
3. **No silent truncation.** Messages are never shortened; anything the default bounds leave out is counted in an `omitted` block.
4. **Deduplicated messages.** In `tree`, each distinct (reason, message) pair is listed once in `reasons[]`, and records refer to it by reason (and by position in `reasons[]` when one reason has several messages). In `events`, grouping already deduplicates messages.
5. **Bounded by default, the same in every format.** Defaults and filters (Pods only under unhealthy PodCliques, Warning events only, `--only-unhealthy`) apply to JSON exactly as to pretty output; flags such as `--pods` and `--all` expand both, and skills choose the flags they need.
6. **Predictable.** Deterministic ordering, integer counts rather than `"n/m"` strings, errors on stderr, and no ANSI codes outside pretty output.
7. **JSON is complete; pretty output is a view.** JSON carries every field a command produces; pretty output may show fewer, never more. The only pretty-only content is notices, such as the events retention note and topology bar graphs. There is therefore no `-o wide`.

The JSON has no `apiVersion`/`kind` envelope, no summary block and no "next step" hints: the root row of a hierarchy acts as the summary, and what to run next is skill knowledge.

**JSON Schema.** The Go output types, with `json` tags and doc comments, are the source of truth. A JSON Schema with enums (`status: Ready | Degraded | Unavailable`), nullability and field descriptions is generated from them at build time and embedded in the binary. Every command with JSON output has a `--schema` flag that prints its schema and exits without contacting the cluster, so the schema always matches the binary and works offline; the schemas are also published in the documentation, and tests validate every command's output against its schema. A flag keeps the schema next to the command that produces it; a `schema` subcommand listing all schemas can be added later.

### Resource References

Commands that take a target accept:

| Ref | Selects |
|---|---|
| `pcs/<name>`, `pcs/<name>/<i>` | a PodCliqueSet, or its replica `i` |
| `pcsg/<name>`, `pcsg/<name>/<i>` | a PodCliqueScalingGroup, or its replica `i` |
| `pclq/<name>` | a PodClique and its Pods |
| `pg/<name>`, `pod/<name>` | a single PodGang or Pod (`events` only) |

With no ref, `tree` and `events` cover every Grove workload in the namespace, and with `-A` in all namespaces; a ref together with `-A` is an error, as in kubectl. A PodGang belongs to a subtree when any of the subtree's Pods is in it, according to the Pod's `grove.io/podgang` label.

### Commands

**Exit codes and permissions.** Commands exit 0 on success, including when they print warnings, and 1 on any error: an unknown ref, an ambiguous topology binding, or a missing permission on a resource the command needs, which the error names (verb, resource and scope). Two cases degrade with a warning instead of failing: when DRA objects cannot be listed, GPU counts fall back to the device plugin; when the operator's Pods cannot be read, `version` reports the operator version as `unavailable`.

**Example cluster.** The examples below use one illustrative cluster: 16 nodes with 8 H200 GPUs each, in racks `rack-a1` and `rack-a2` (block `block-a`) and `rack-b1` and `rack-b2` (block `block-b`), described by the ClusterTopologyBinding `h200-cluster`. Node `h200-08` has been cordoned and drained. The workload `llm-serve` has two replicas; each has a `frontend` clique and a `decode` scaling group with two replicas (`minAvailable: 1`), each decode replica packed into one rack with a `leader` (1 Pod × 4 GPUs) and a `worker` (2 Pods × 4 GPUs). Draining `h200-08` evicted two `worker` Pods of decode replica 1 in `llm-serve` replica 1; Grove gang-terminated that decode replica after its `terminationDelay` (set to 5 minutes; the default is 4 hours) and recreated it, and its three new Pods (12 GPUs) cannot be placed because no rack has more than 8 GPUs free. Names, numbers and scheduler messages are illustrative.

#### `tree`

```text
kubectl grove tree [ref] [--only-unhealthy] [--pods] [--depth N] [-A]
```

Shows the Grove hierarchy with health rolled up from Pods. Each row is a PCS, a PCS replica, a PCSG, a PCSG replica or a PCLQ, with a status, the worst reason beneath it and how many Pods have that reason, ready and desired replicas, and GPUs allocated and requested (the full Grove/other/free split is `topology`'s job).

**Status** is `Ready`, `Degraded` (the minimum is met but the row is not fully up) or `Unavailable`:

| Row | Ready | Unavailable | Degraded |
|---|---|---|---|
| PodClique | all desired Pods ready | fewer ready Pods than its `minAvailable` | otherwise |
| PCS or PCSG replica | all children `Ready` | any child `Unavailable` | otherwise |
| PodCliqueScalingGroup | all replicas `Ready` | fewer than `minAvailable` replicas are `Ready` or `Degraded` | otherwise |
| PodCliqueSet | all replicas `Ready` | every replica `Unavailable` | otherwise (a PCS has no `minAvailable` across replicas) |

**Reasons** are Kubernetes' own: a container's waiting or terminated reason (`ErrImagePull`, `CrashLoopBackOff`), otherwise the reason of the Pod's `PodScheduled` condition (`Unschedulable`, `SchedulingGated`), otherwise `Pending` or `NotReady`. Worst first: Pods that could not be created (below), container crash and image errors, unschedulable, pending (including Pods held at a scheduling gate), not ready; ties are broken by Pod count, then by name.

**Pods that cannot be created.** If the API server rejects a PodClique's Pods (an invalid environment variable name, an invalid volume mount, an exceeded ResourceQuota), the clique has no Pods to roll up: the cause is only in its `status.lastErrors` (`code: ERR_CREATE_POD`) and its events, while the Pods of sibling cliques wait at a scheduling gate for a gang that cannot form. While such a PodClique has fewer Pods than desired and its `lastErrors` contain `ERR_CREATE_POD`, it gets the reason `PodCreateFailed`, counting the missing Pods, which outranks every Pod reason, so `tree` points at the failing clique rather than at its waiting siblings.

**Ready replicas** are ready Pods for a PodClique and Grove's `availableReplicas` for PodCliqueSets and scaling groups, which is why a `Degraded` PodCliqueSet can read `2/2`: Grove counts a replica as available while its scaling groups meet `minAvailable`, and the roll-up shows what that hides.

**Bounds.** Pods are listed only under unhealthy PodCliques; `--pods` lists all. `--only-unhealthy` hides healthy branches and `--depth N` limits the depth in rows (replica rows included); what either leaves out is counted in `omitted`. Pretty output collapses levels with a single replica; JSON always contains replica records, so a record's `parent` does not change when a workload scales from one replica to two. Pretty output uses short group names, full Pod names and a NODE column on Pod rows, and lists each distinct message once in a trailing "Reasons" section. PodGangs are not a level of the tree, because a Pod's gang mostly follows from its position; JSON Pod records carry a `gang` taken from the Pod's `grove.io/podgang` label.

```text
$ kubectl grove tree pcs/llm-serve -n inference
NAME                                              KIND  READY  GPUS   STATUS                               NODE
llm-serve                                         PCS   2/2    36/48  Degraded: Unschedulable (3 pods)
├─ [0]                                                         24/24  Ready
│  ├─ frontend                                    PCLQ  1/1    0/0    Ready
│  └─ decode                                      PCSG  2/2    24/24  Ready
│     ├─ [0]                                                   12/12  Ready
│     │  ├─ leader                                PCLQ  1/1    4/4    Ready
│     │  └─ worker                                PCLQ  2/2    8/8    Ready
│     └─ [1]                                                   12/12  Ready
│        ├─ leader                                PCLQ  1/1    4/4    Ready
│        └─ worker                                PCLQ  2/2    8/8    Ready
└─ [1]                                                         12/24  Degraded: Unschedulable (3 pods)
   ├─ frontend                                    PCLQ  1/1    0/0    Ready
   └─ decode                                      PCSG  1/2    12/24  Degraded: Unschedulable (3 pods)
      ├─ [0]                                                   12/12  Ready
      │  ├─ leader                                PCLQ  1/1    4/4    Ready
      │  └─ worker                                PCLQ  2/2    8/8    Ready
      └─ [1]                                                   0/12   Unavailable: Unschedulable (3 pods)
         ├─ leader                                PCLQ  0/1    0/4    Unavailable: Unschedulable (1 pod)
         │  └─ llm-serve-1-decode-1-leader-x7k2p  Pod          0/4    Unschedulable                        <none>
         └─ worker                                PCLQ  0/2    0/8    Unavailable: Unschedulable (2 pods)
            ├─ llm-serve-1-decode-1-worker-m4q8z  Pod          0/4    Unschedulable                        <none>
            └─ llm-serve-1-decode-1-worker-w9d4f  Pod          0/4    Unschedulable                        <none>

Reasons:
  Unschedulable (3 pods): pod group needs 12 nvidia.com/gpu within one rack; max free in any rack: 8

11 healthy pods hidden (--pods to list pods)
```

The same command with `--only-unhealthy -o json`:

```json
{
  "namespace": "inference",
  "groups": [
    {"ref": "pcs/llm-serve", "parent": null, "readyReplicas": 2, "replicas": 2, "gpusAllocated": 36, "gpusRequested": 48, "status": "Degraded", "reason": "Unschedulable", "count": 3},
    {"ref": "pcs/llm-serve/1", "parent": "pcs/llm-serve", "readyReplicas": null, "replicas": null, "gpusAllocated": 12, "gpusRequested": 24, "status": "Degraded", "reason": "Unschedulable", "count": 3},
    {"ref": "pcsg/llm-serve-1-decode", "parent": "pcs/llm-serve/1", "readyReplicas": 1, "replicas": 2, "gpusAllocated": 12, "gpusRequested": 24, "status": "Degraded", "reason": "Unschedulable", "count": 3},
    {"ref": "pcsg/llm-serve-1-decode/1", "parent": "pcsg/llm-serve-1-decode", "readyReplicas": null, "replicas": null, "gpusAllocated": 0, "gpusRequested": 12, "status": "Unavailable", "reason": "Unschedulable", "count": 3},
    {"ref": "pclq/llm-serve-1-decode-1-leader", "parent": "pcsg/llm-serve-1-decode/1", "readyReplicas": 0, "replicas": 1, "gpusAllocated": 0, "gpusRequested": 4, "status": "Unavailable", "reason": "Unschedulable", "count": 1},
    {"ref": "pclq/llm-serve-1-decode-1-worker", "parent": "pcsg/llm-serve-1-decode/1", "readyReplicas": 0, "replicas": 2, "gpusAllocated": 0, "gpusRequested": 8, "status": "Unavailable", "reason": "Unschedulable", "count": 2}
  ],
  "pods": [
    {"ref": "pod/llm-serve-1-decode-1-leader-x7k2p", "parent": "pclq/llm-serve-1-decode-1-leader", "node": null, "ready": false, "gpusAllocated": 0, "gpusRequested": 4, "gang": "pg/llm-serve-1-1793093400004182338-decode-1", "reason": "Unschedulable"},
    {"ref": "pod/llm-serve-1-decode-1-worker-m4q8z", "parent": "pclq/llm-serve-1-decode-1-worker", "node": null, "ready": false, "gpusAllocated": 0, "gpusRequested": 4, "gang": "pg/llm-serve-1-1793093400004182338-decode-1", "reason": "Unschedulable"},
    {"ref": "pod/llm-serve-1-decode-1-worker-w9d4f", "parent": "pclq/llm-serve-1-decode-1-worker", "node": null, "ready": false, "gpusAllocated": 0, "gpusRequested": 4, "gang": "pg/llm-serve-1-1793093400004182338-decode-1", "reason": "Unschedulable"}
  ],
  "reasons": [
    {"reason": "Unschedulable", "count": 3, "message": "pod group needs 12 nvidia.com/gpu within one rack; max free in any rack: 8"}
  ],
  "omitted": {"healthyGroups": 13, "healthyPods": 11, "belowDepthGroups": 0, "belowDepthPods": 0}
}
```

JSON shape: `namespace`; `groups[]` with `ref`, `parent`, `readyReplicas`, `replicas`, `gpusAllocated`, `gpusRequested`, `status`, `reason`, `count` (`readyReplicas` and `replicas` are `null` on replica rows); `pods[]` with `ref`, `parent`, `node`, `ready`, `gpusAllocated`, `gpusRequested`, `gang`, `reason`; `reasons[]` with `reason`, `count`, `message`; `omitted` with `healthyGroups`, `healthyPods`, `belowDepthGroups`, `belowDepthPods`. With `-A`, the top-level `namespace` is `null` and every record carries its own.

From the POC scenario with an invalid environment variable name, where the `api` clique's Pods are rejected:

```text
$ kubectl grove tree pcs/container-error-invalid-env
NAME                                              KIND  READY  GPUS  STATUS                                 NODE
container-error-invalid-env                       PCS   0/1    0/0   Unavailable: PodCreateFailed (2 pods)
├─ api                                            PCLQ  0/2    0/0   Unavailable: PodCreateFailed (2 pods)
├─ web                                            PCLQ  0/2    0/0   Unavailable: SchedulingGated (2 pods)
│  ├─ container-error-invalid-env-0-web-2mqrt     Pod          0/0   SchedulingGated                        <none>
│  └─ container-error-invalid-env-0-web-x8kzn     Pod          0/0   SchedulingGated                        <none>
└─ worker                                         PCLQ  0/1    0/0   Unavailable: SchedulingGated (1 pod)
   └─ container-error-invalid-env-0-worker-b5wd9  Pod          0/0   SchedulingGated                        <none>

Reasons:
  PodCreateFailed (2 pods): failed to sync Pod: [Operation: Sync, Code: ERR_CREATE_POD] message: failed to create Pod:  for PodClique default/container-error-invalid-env-0-api, cause: Pod "container-error-invalid-env-0-api-7p8f2" is invalid: spec.containers[0].env[0].name: Invalid value: "KEY=1": a valid environment variable name must consist only of printable ASCII characters other than '='
  SchedulingGated (3 pods): Scheduling is blocked due to non-empty scheduling gates
```

#### `events`

```text
kubectl grove events [ref] [--all] [--no-group] [--since DURATION] [--reverse] [-A]
```

Shows the events of a Grove subtree, grouped. A subtree's events are those of its Grove resources and Pods: PCS, PCSG, PCLQ, PodGang and Pod. Events of other kinds (HPAs, ResourceClaims, Services, RBAC objects, Secrets, Nodes, scheduler-specific objects) are not included; see [Alternatives](#events-scope) and [Open Questions](#open-questions). A single PodGang or Pod is accepted as a target too, although for a Pod this duplicates `kubectl events --for`, so that users can stay in one tool.

**Matching.** Events are matched to the subtree's objects by UID, because name prefixes are ambiguous and a UID separates a recreated object from its predecessor. Two fallbacks keep events of objects that have been replaced, flagged `objectDeleted: true`:

- **Deleted Pods.** An event for an unknown Pod UID is included if the Pod's name is a PodClique in the subtree followed by the suffix Kubernetes generates for Grove's `GenerateName: "<podclique>-"` (five characters from `bcdfghjklmnpqrstvwxz2456789`). It is attributed to that PodClique.
- **Recreated Grove resources.** Gang termination deletes a scaling-group replica's PodCliques and recreates them under the same names with new UIDs. An event whose object kind and name match a live Grove object in the subtree, but whose UID does not, is attributed to the live object.

**Why include earlier incarnations?** When Grove recreates an object in place, the earlier object's events usually explain the recreation; the `AllScheduledReplicasLost` event below is what armed the gang termination. Unlike name matching, Arborist marks such events, so they cannot be mistaken for the current object's. Grove resources that no longer exist under any live name are not recovered.

**Scope.** Only the subtree's own objects are included, not its ancestors. Grove records some events about a child on its parent (for example `PodCliqueCreateOrUpdateFailed` on the PodCliqueSet or scaling group that failed to create a PodClique), so pretty output names the ancestors that were excluded (JSON: `ancestorsExcluded: true`), and the skills start at the PodCliqueSet before narrowing.

**Filtering.** Only `Warning` events are shown by default; `--all` adds `Normal` events, and the hidden ones are counted (`omitted.normalEvents`, in event records; pretty: "N Normal events hidden (--all)"). Kubernetes has only these two event types, so there is no severity flag. `--since` filters on when an event was last seen; by default every event the cluster retains is shown.

**Grouping.** Events are grouped by `(type, reason, source, message)`. Each group has a summed `count`, `firstSeen`, `lastSeen`, its `objects` (`{ref, count}`, plus `objectDeleted: true` where it applies) and a `scope`: the lowest common Grove ancestor of the objects, for example `pcsg/x/1` when all affected Pods are in that replica (a deleted Pod counts as part of its PodClique). An event about a single object is a group of one; `--no-group` prints one row per object instead. Before grouping, names of Pods generated for a PodClique in the subtree are replaced in messages with `<podclique>-*`, because the API server's rejection quoted in Grove's `PodCreateFailed` events names each generated Pod and would otherwise make one group per attempt; the `(combined from similar events): ` prefix that client-go adds to repeated events is removed for the same reason. `--no-group` shows the original messages. Nothing else is normalised: messages with different numbers (`0/16` vs `0/15 nodes`) form separate groups, because the numbers can matter. With `-A`, events from different namespaces are never grouped together.

**Ordering and output.** Groups are sorted newest first by `lastSeen`, so what is failing now is on top; `--reverse` gives the oldest first, like `kubectl events`, which is often where the root cause is. JSON lists every object in every group. Pretty output shows the count and scope instead of object names, because a 256-Pod PodClique failing one way would otherwise print 256 names; `--no-group` and `-o json` give the names. Pretty columns are LAST SEEN, FIRST SEEN, TYPE, REASON, SOURCE, COUNT and SCOPE, with the message on the following line, and the output always ends with the retention notice.

```text
$ kubectl grove events pcs/llm-serve -n inference
LAST SEEN  FIRST SEEN  TYPE     REASON                    SOURCE                COUNT  SCOPE
12s        41m         Warning  Unschedulable             kai-scheduler         27     pcsg/llm-serve-1-decode/1 (3 pods)
           pod group needs 12 nvidia.com/gpu within one rack; max free in any rack: 8
42m        47m         Warning  Unschedulable             kai-scheduler         12     pclq/llm-serve-1-decode-1-worker (2 deleted pods)
           pod group needs 8 nvidia.com/gpu in rack rack-a2; free in rack: 0
42m        46m         Warning  Unhealthy                 kubelet               14     pclq/llm-serve-1-decode-1-leader (1 deleted pod)
           Readiness probe failed: Get "http://10.244.6.21:8000/health": dial tcp 10.244.6.21:8000: connect: connection refused
47m        47m         Warning  AllScheduledReplicasLost  podclique-controller  1      pclq/llm-serve-1-decode-1-worker (earlier incarnation)
           All scheduled pods lost (was 2). Gang termination will fire after TerminationDelay if the PodClique stays below MinAvailable; investigate node availability or capacity.
41 Normal events hidden (--all).
Events are retained only for the cluster's event TTL (default 1h); absence of events does not mean nothing happened.
```

Read oldest first, the groups tell the story: the `worker` PodClique lost its scheduled Pods when `h200-08` was drained; their replacements could not be placed in the leader's rack; the surviving leader failed its readiness probe until Grove gang-terminated the decode replica; and the recreated replica cannot be scheduled.

```json
{
  "namespace": "inference",
  "ancestorsExcluded": false,
  "groups": [
    {
      "type": "Warning", "reason": "Unschedulable", "source": "kai-scheduler",
      "message": "pod group needs 12 nvidia.com/gpu within one rack; max free in any rack: 8",
      "count": 27, "firstSeen": "2026-10-29T13:18:02Z", "lastSeen": "2026-10-29T13:59:48Z",
      "scope": "pcsg/llm-serve-1-decode/1",
      "objects": [
        {"ref": "pod/llm-serve-1-decode-1-leader-x7k2p", "count": 9},
        {"ref": "pod/llm-serve-1-decode-1-worker-m4q8z", "count": 9},
        {"ref": "pod/llm-serve-1-decode-1-worker-w9d4f", "count": 9}
      ]
    },
    {
      "type": "Warning", "reason": "Unschedulable", "source": "kai-scheduler",
      "message": "pod group needs 8 nvidia.com/gpu in rack rack-a2; free in rack: 0",
      "count": 12, "firstSeen": "2026-10-29T13:12:42Z", "lastSeen": "2026-10-29T13:17:38Z",
      "scope": "pclq/llm-serve-1-decode-1-worker",
      "objects": [
        {"ref": "pod/llm-serve-1-decode-1-worker-q5t8v", "count": 6, "objectDeleted": true},
        {"ref": "pod/llm-serve-1-decode-1-worker-z2hlc", "count": 6, "objectDeleted": true}
      ]
    },
    {
      "type": "Warning", "reason": "Unhealthy", "source": "kubelet",
      "message": "Readiness probe failed: Get \"http://10.244.6.21:8000/health\": dial tcp 10.244.6.21:8000: connect: connection refused",
      "count": 14, "firstSeen": "2026-10-29T13:13:05Z", "lastSeen": "2026-10-29T13:17:31Z",
      "scope": "pclq/llm-serve-1-decode-1-leader",
      "objects": [
        {"ref": "pod/llm-serve-1-decode-1-leader-h5vtn", "count": 14, "objectDeleted": true}
      ]
    },
    {
      "type": "Warning", "reason": "AllScheduledReplicasLost", "source": "podclique-controller",
      "message": "All scheduled pods lost (was 2). Gang termination will fire after TerminationDelay if the PodClique stays below MinAvailable; investigate node availability or capacity.",
      "count": 1, "firstSeen": "2026-10-29T13:12:41Z", "lastSeen": "2026-10-29T13:12:41Z",
      "scope": "pclq/llm-serve-1-decode-1-worker",
      "objects": [
        {"ref": "pclq/llm-serve-1-decode-1-worker", "count": 1, "objectDeleted": true}
      ]
    }
  ],
  "omitted": {"normalEvents": 41}
}
```

In the invalid environment variable scenario, each attempt to create a Pod records a `PodCreateFailed` event naming a different generated Pod; masking keeps them in one group:

```text
$ kubectl grove events pcs/container-error-invalid-env
LAST SEEN  FIRST SEEN  TYPE     REASON           SOURCE                COUNT  SCOPE
25s        26s         Warning  PodCreateFailed  podclique-controller  9      pclq/container-error-invalid-env-0-api
           Error creating pod : Pod "container-error-invalid-env-0-api-*" is invalid: spec.containers[0].env[0].name: Invalid value: "KEY=1": a valid environment variable name must consist only of printable ASCII characters other than '='
3 Normal events hidden (--all).
Events are retained only for the cluster's event TTL (default 1h); absence of events does not mean nothing happened.
```

**Data access.** For each namespace (or for all namespaces with `-A`), `events` lists the Grove resources and Grove Pods with the loader shared with `tree`, then makes one list of `events.k8s.io/v1` events and matches them in memory; it never makes one request per object. Grove's controllers and the kubelet write events through the older `core/v1` API, so counts and times come from the `deprecatedCount`, `deprecatedFirstTimestamp` and `deprecatedLastTimestamp` fields when `series` and `eventTime` are empty.

#### `topology`

```text
kubectl grove topology [level] [-t BINDING] [--in LEVEL=VALUE,...] [--for REF]
```

Shows GPU capacity grouped by the domains of one level of a [ClusterTopologyBinding](../244-topology-aware-scheduling/README.md), joining the binding's levels with node labels and GPU capacity and with the placement and GPU requests of every Pod in the cluster.

**Selecting the binding.** `-t/--topology <name>` selects the binding (the object a workload names in its `topologyName`), much as `-n` selects a namespace; a level is one of the binding's `spec.levels[].domain` values. If `-t` is omitted and exactly one binding exists, it is used; if several exist, the command fails and lists their names. `--for` does not select the binding, but a warning is printed if the referenced workload uses a different one. Skills always pass `-t`, so they keep working when a second binding is added. Without any binding, `topology` groups by node and says so. Listing bindings and their levels is left to `kubectl get ctb`.

**Overview.** With no level, `topology` lists the binding's levels with their node label key and the number of distinct values present on nodes, plus cluster-wide totals per GPU type. A level with 0 values points to a label key that no node carries, a mistake `kubectl get ctb` cannot show:

```text
$ kubectl grove topology
Topology: h200-cluster (only ClusterTopologyBinding; use -t to select one explicitly)
LEVEL  KEY                          VALUES
zone   topology.kubernetes.io/zone  1
block  example.com/block            2
rack   example.com/rack             4
host   kubernetes.io/hostname       16
GPUs (H200): 128 capacity — grove 52, other 44, free 24, unavailable 8
```

**Rows.** With a level (`topology rack`), each row is a (domain value, GPU type) pair; the GPU type is the node's `nvidia.com/gpu.product` label without its vendor prefix (`NVIDIA-H200` becomes `H200`), or `unknown`. Columns:

| Column | Definition |
|---|---|
| **Capacity** | Sum of the nodes' allocatable GPUs, plus DRA GPUs from their ResourceSlices |
| **Grove** | GPUs requested by Grove Pods on those nodes |
| **Other** | GPUs requested by all other Pods on those nodes |
| **Free** | Capacity − Grove − Other − Unavailable, floored at 0 |
| **Unavailable** | Unallocated GPUs on cordoned or NotReady nodes |

Terminal Pods (`Succeeded`, `Failed`) are excluded, and taints are ignored (see [Risks](#incomplete-gpu-accounting)). Nodes without the level's label form a `(none)` row (JSON `value: null`), so rows always add up to the totals; with the misconfigured key above, every node would appear under `(none)`. Each JSON row carries `path`, the values of its broader levels, so an agent can build the next `--in` without parsing the topology. At the lowest level, pretty output marks nodes that are `cordoned` or `NotReady`, and JSON rows carry the same as a `nodeState` list, plus the Node's `ref`, since a label value such as `kubernetes.io/hostname` can differ from the Node name.

**Unscheduled demand.** A footer line counts non-terminal Pods that request GPUs but have no node: the Pods and the GPUs they request, split into Grove and other, without a GPU type. Pods held at a scheduling gate (such as Grove's `grove.io/podgang-pending-creation`) are not yet asking for placement, so they are counted only as `gatedPods`. The footer is not narrowed by `--in`, since these Pods have no node.

```text
$ kubectl grove topology rack -t h200-cluster
RACK     TYPE  CAPACITY  GROVE  OTHER  FREE  UNAVAIL
rack-a1  H200  32        24     4      4     0        [▓▓▓▓▓▓▓▓▓▓▓▓░░  ]
rack-a2  H200  32        12     8      4     8        [▓▓▓▓▓▓░░░░  ××××]
rack-b1  H200  32        16     8      8     0        [▓▓▓▓▓▓▓▓░░░░    ]
rack-b2  H200  32        0      24     8     0        [░░░░░░░░░░░░    ]
Unscheduled: 3 pods requesting 12 GPUs (grove 12, other 0); 0 scheduling-gated pods
```

Bars are scaled to each row's capacity: `▓` Grove, `░` other, blank free, `×` unavailable. 24 GPUs are free, but no rack has more than 8, while the unscheduled gang needs 12 in one rack: fragmentation, not a shortage.

```json
{
  "topology": "h200-cluster",
  "level": "rack",
  "rows": [
    {"value": "rack-a1", "path": {"zone": "zone-1", "block": "block-a"}, "gpuType": "H200", "capacity": 32, "grove": 24, "other": 4, "free": 4, "unavailable": 0},
    {"value": "rack-a2", "path": {"zone": "zone-1", "block": "block-a"}, "gpuType": "H200", "capacity": 32, "grove": 12, "other": 8, "free": 4, "unavailable": 8},
    {"value": "rack-b1", "path": {"zone": "zone-1", "block": "block-b"}, "gpuType": "H200", "capacity": 32, "grove": 16, "other": 8, "free": 8, "unavailable": 0},
    {"value": "rack-b2", "path": {"zone": "zone-1", "block": "block-b"}, "gpuType": "H200", "capacity": 32, "grove": 0, "other": 24, "free": 8, "unavailable": 0}
  ],
  "unscheduled": {"pods": 3, "gpusRequested": 12, "grove": 12, "other": 0, "gatedPods": 0}
}
```

**Narrowing.** `--in level=value,...` keeps only nodes matching all pairs, for example `topology rack --in zone=zone-1` or `topology host --in rack=rack-a2`. Levels in `--in` must be broader (earlier in the binding's level list) than the displayed level; intermediate levels may be skipped; an unknown level or value is an error that lists the valid ones.

```text
$ kubectl grove topology host -t h200-cluster --in rack=rack-a2
HOST     TYPE  CAPACITY  GROVE  OTHER  FREE  UNAVAIL
h200-05  H200  8         8      0      0     0        [▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓]
h200-06  H200  8         4      4      0     0        [▓▓▓▓▓▓▓▓░░░░░░░░]
h200-07  H200  8         0      4      4     0        [░░░░░░░░        ]
h200-08  H200  8         0      0      0     8        [××××××××××××××××]  cordoned
Unscheduled: 3 pods requesting 12 GPUs (grove 12, other 0); 0 scheduling-gated pods
```

The host rows add up to the `rack-a2` row; uncordoning `h200-08` would give `rack-a2` exactly the 12 free GPUs the gang needs.

**Workload placement.** `--for <ref>` takes one PCS, PCSG or PCLQ ref, replica refs included, and adds a column, labelled with the ref, counting the GPUs of that subtree's Pods in each row (a subset of Grove; the other columns are unchanged). The unscheduled footer is scoped to the same subtree. JSON adds a top-level `for`, the ref's `namespace`, and `selectedGPUs` on each row.

```text
$ kubectl grove topology rack -t h200-cluster --for pcs/llm-serve/1 -n inference
RACK     TYPE  CAPACITY  GROVE  [pcs/llm-serve/1]  OTHER  FREE  UNAVAIL
rack-a1  H200  32        24     0                  4      4     0        [▓▓▓▓▓▓▓▓▓▓▓▓░░  ]
rack-a2  H200  32        12     12                 8      4     8        [▓▓▓▓▓▓░░░░  ××××]
rack-b1  H200  32        16     0                  8      8     0        [▓▓▓▓▓▓▓▓░░░░    ]
rack-b2  H200  32        0      0                  24     8     0        [░░░░░░░░░░░░    ]
Unscheduled (pcs/llm-serve/1): 3 pods requesting 12 GPUs; 0 scheduling-gated pods
```

Replica 1's placed GPUs, those of its decode replica 0, are all in `rack-a2`. Per-replica packing constraints apply to scaling-group replicas, so `--for pcsg/llm-serve-1-decode/0` is the ref that checks that this decode replica honoured its rack packing.

**Listing Pods per row (optional, not in the initial implementation).** A `--pods` flag would list the GPU-requesting Pods under each row (ref, node, GPUs and, for Grove Pods, the parent PodClique), with `--for` only that subtree's Pods, in a separate JSON `pods[]` array. It is described here to collect feedback on its shape.

#### `version`

```text
kubectl grove version [--grove-namespace NS]
```

Prints the client version, the Grove operator version and a link to the operator's exact source. It earns a command despite the minimal-CLI rule because the client version is known only to the binary, and a skew check needs both sides. It looks up Grove's version only; versions of other components, such as the scheduler, are left to the agent, which can read them from their images.

**Finding the operator version** needs no change to Grove:

1. Find the operator Pods by the label `app.kubernetes.io/name=grove-operator`, which the Helm chart sets: in `--grove-namespace` if given, otherwise across all namespaces. The chart installs into any namespace, so there is no fixed default.
2. The version is the operator container's image tag, ignoring any `@sha256:` digest suffix. If the image is referenced by digest alone, the version comes from the Pod's `chart: grove-charts-<version>` label, which carries the release version for charts published by Grove's CI (charts installed from the source tree carry `0.1.0`).
3. If the Pods run more than one version (a rollout is in progress), each version is listed with a warning.

This is enough because Grove's CI uses one `PACKAGE_VERSION` for the image tag, the operator binary's version and the chart's version: the release tag for releases and `v0.0.0-g<short sha>` for every commit pushed to `main`. The tag is part of the running Pod's spec, so it describes what is actually running, however Grove was deployed (Helm, `helm template` or GitOps). Locally built images (tagged from `operator/VERSION`, for example `v0.1.0-dev`) and custom tags report `unknown`.

**Skew.** A CLI supports operators of the same major and minor version; patch versions may differ. When major or minor differ, `version` prints a warning on stderr that names the operator's release and where to download the matching CLI, and still exits 0. Only `version` checks skew: other commands do not read the operator version, so they keep working when it cannot be read. This matches `kubectl version`, `velero version` and `linkerd check`; none of the surveyed CLIs warns on every command (see [Appendix](#version-detection-prior-art)). The JSON has no skew field; consumers compare the two versions.

**Source link.** A release version `vX.Y.Z`, including pre-releases such as `v0.1.0-alpha.17` but not the `-dev` suffix of local builds, links to `https://github.com/ai-dynamo/grove/archive/refs/tags/<tag>.tar.gz`; `v0.0.0-g<sha>` links to `https://github.com/ai-dynamo/grove/archive/<sha>.tar.gz`; anything else is `unknown`, and `main` is never substituted. Arborist only prints the link; it makes no request outside the cluster.

```text
$ kubectl grove version
kubectl grove   v0.6.0
Grove operator  v0.6.0  (pod grove-system/grove-operator-6c9f7b8d5-q4lzt)
Grove source    https://github.com/ai-dynamo/grove/archive/refs/tags/v0.6.0.tar.gz
                git clone --depth 1 --branch v0.6.0 https://github.com/ai-dynamo/grove
```

```json
{
  "client": {"version": "v0.6.0"},
  "operator": {
    "version": "v0.6.0",
    "namespace": "grove-system",
    "pods": [
      {"ref": "pod/grove-operator-6c9f7b8d5-q4lzt", "image": "ghcr.io/ai-dynamo/grove/grove-operator:v0.6.0", "version": "v0.6.0"}
    ]
  },
  "source": {
    "url": "https://github.com/ai-dynamo/grove/archive/refs/tags/v0.6.0.tar.gz",
    "git": "git clone --depth 1 --branch v0.6.0 https://github.com/ai-dynamo/grove"
  }
}
```

With a v0.5.2 client, stderr reads `warning: kubectl grove v0.5.2 is not compatible with Grove operator v0.6.0; download kubectl grove v0.6 from https://github.com/ai-dynamo/grove/releases/tag/v0.6.0`. When the operator's Pods cannot be read, the operator version is `unavailable` and the source `unknown`, with a stderr hint naming the missing permission and suggesting `--grove-namespace`.

#### `skills install`

```text
kubectl grove skills install [DIR]
```

Writes the skills embedded in the binary into an agent's skills directory, so the whole install for an operator is `kubectl krew install grove && kubectl grove skills install`, and the skills always match the binary whose flags and JSON fields they describe. `DIR` is a positional argument; there are no flags and no `-o json`.

- If `DIR` is omitted and stdin is a terminal, the user chooses from known agent skill directories (`~/.claude/skills/`, `~/.codex/skills/`, `~/.agents/skills/`) or enters another path.
- If `DIR` is omitted and stdin is not a terminal (an agent or script is running it), the command fails and lists those choices.
- It creates `DIR` if needed, replaces only the `grove-*` skill folders it ships (re-running it upgrades them), leaves other folders alone, and prints one line per folder written.
- There are no `list`, `uninstall` or `update` subcommands.

```text
$ kubectl grove skills install
Where should the Grove skills be installed?
  1) ~/.claude/skills
  2) ~/.codex/skills
  3) ~/.agents/skills
  4) Other path…
> 3
Installed Grove skills v0.6.0:
  /home/alice/.agents/skills/grove-debug
  /home/alice/.agents/skills/grove-topology
  /home/alice/.agents/skills/grove-diagnostics
```

The skill sources are ordinary `SKILL.md` folders, so copying them by hand or installing them with other skill installers also works.

### GPU Accounting

`tree` and `topology` share one GPU accounting that supports both allocation models from the start:

- **Device plugin:** node allocatable and Pod requests for `nvidia.com/gpu`. A Pod's request is its effective request as the scheduler computes it: the larger of the sum over its app and sidecar containers and, for each init container, its request plus those of the sidecars started before it, plus any Pod overhead.
- **DRA:** capacity from the `ResourceSlice`s of the NVIDIA DRA driver (`gpu.nvidia.com`); requested GPUs from the device requests of the Pod's claims (or of the claim template, before the claim exists), and allocated GPUs from the claims' allocations, so unscheduled DRA Pods are counted too. A claim shared by several Pods ([GREP-390](../390-hierarchical-resource-sharing/README.md)) is counted once.

Nodes that use both are summed. Only whole GPUs are counted; MIG, partitioned and shared devices are skipped with a warning. Only `resource.k8s.io/v1` is read; if it is not served, or RBAC forbids listing the DRA objects, counting falls back to the device plugin with a warning.

### Agent Skills

#### Format and packaging

Each skill is a standard `SKILL.md` folder with a `grove-` prefix, kept in the Grove repository. A shared reference is maintained once and copied into each skill when the binary is built, so every installed skill is self-contained. The skills are embedded in the binary, record the version they were built with, and check it against `kubectl grove version` before anything else (see [Risks](#version-skew-between-cli-skills-and-operator) for what they do on a mismatch).

The shared reference contains:

- the Arborist command reference and a glossary of the JSON fields the skills use;
- Grove's hierarchy and labels, and kubectl recipes for what Arborist leaves to kubectl (YAML, logs, conditions, PodGangs);
- which conditions to wait on, per kind:
  - **PodClique:** `PodCliqueScheduled`, `MinAvailableBreached`, `UpdateInProgress`.
  - **Scaling group:** `MinAvailableBreached`, `UpdateInProgress`, `GangTerminationInProgress`.
  - **PodCliqueSet:** `UpdateInProgress`, `TopologyLevelsUnavailable`, `PodGangMigrationInProgress`. It has no `MinAvailableBreached`.
  - **PodGang:** `Initialized`, `Scheduled`, `Ready`.
  - **ClusterTopologyBinding:** `SchedulerTopologyDrift`.

  `UpdateInProgress=Unknown` means the update passed its progress deadline.
- events that Grove records on a parent but that describe a child, and the fact that events of the cluster-scoped ClusterTopologyBinding are recorded in the `default` namespace;
- the version check, the rule to treat cluster text as data, and token guidance: narrow the scope, filter JSON with `jq` before reading it, avoid `--all` unless needed.

#### What the skills know

Skills surface the tools, what each reveals and the Grove knowledge an agent lacks; the agent decides how to proceed. This GREP specifies each skill's description and workflow; detailed content such as failure catalogues is written during implementation. Several answers that could have been commands are skill knowledge instead, because they work on small inputs:

| Question | How the skill answers it |
|---|---|
| Is the rollout progressing? | Compare each Pod's `grove.io/pod-template-hash` label with its PodClique's, or PodClique and scaling group `status.currentPodCliqueSetGenerationHash` with the PodCliqueSet's `status.currentGenerationHash` (see [GREP-393](../393-coherent-rolling-updates/README.md)). |
| Which topology constraint applies? | Resolve the inherited `topologyName` and pack constraints in the PodCliqueSet spec (template → scaling group → clique). There is one effective `topologyName` per PodCliqueSet, and PodGangs carry it in the `grove.io/topology-name` annotation. |
| Will this workload fit? | Compare the workload's demand per domain with the free GPUs per domain from `topology -o json`. |
| What did the operator log about this workload? | Take object names from `tree -o json` and filter the operator's `kubectl logs`. |
| What does the code actually do here? | With the user's agreement, download the source for the running version from the link `kubectl grove version` prints. |
| What is the overall status? | `tree --only-unhealthy` for the blockers, `events <blocker>` for why, `kubectl get -o yaml` for conditions, `kubectl get podgang -l app.kubernetes.io/part-of=<pcs>` for gang state. |

#### `grove-debug`

**Description:** diagnose unhealthy or stuck Grove workloads (PodCliqueSets, scaling groups, PodCliques): pending, unschedulable, crashing, image errors, rollouts not completing. Uses `kubectl grove` and kubectl. Read-only.

**Workflow:**

1. **Preflight:** check that `kubectl grove` is installed and compatible with the operator and that Grove's CRDs exist, and resolve the target (namespace-wide `tree --only-unhealthy` if no PodCliqueSet is named).
2. **Locate:** `tree <ref> --only-unhealthy`. The deepest unhealthy groups are the blockers.
3. **Explain:** `events <blocker>`.
4. **Dig in** as the evidence suggests: image errors, crashes (`kubectl logs --previous`), configuration and mount errors, Pods that could not be created, unschedulable Pods (PodGangs and the effective topology constraint, handing placement questions to `grove-topology`), gated Pods and gang assembly, stuck rollouts (template hashes), operator logs. When cluster state does not explain the behaviour, suggest reading the exact Grove source for the running version, and where relevant the source of other components such as the scheduler at the versions found in their images, downloading only with the user's agreement.
5. **Report** the root cause, the evidence and the affected objects.
6. **Wait**, when asked to, with `kubectl wait --for=condition=...`, and locate again on a timeout.

#### `grove-topology`

**Description:** understand Grove topology setup and GPU placement: whether a ClusterTopologyBinding matches the nodes and is in sync with the scheduler, where GPU capacity is free, how fragmented it is, whether a workload fits, and where a workload or replica landed. Uses `kubectl grove topology` and kubectl. Read-only.

**Workflow:**

1. **Select** the binding: `kubectl get ctb`, and the binding a workload uses through the `topologyName` in its PodCliqueSet spec.
2. **Check health:** `topology -t <ctb>` (a level with 0 values means a wrong label key); `kubectl get ctb -o yaml` for scheduler sync (`schedulerTopologyStatuses[].inSync` and the `SchedulerTopologyDrift` condition); the PodCliqueSet's `TopologyLevelsUnavailable` condition and its reason, such as `ClusterTopologyNotFound` or `TopologyNameMissing`.
3. **Find capacity:** `topology <level>`, narrowed with `--in`, comparing free GPUs with unscheduled demand to tell fragmentation from shortage, and checking taints when capacity looks free but Pods stay pending.
4. **Check placement:** `topology <level> --for <ref>`, with scaling-group replica refs to check per-replica packing.
5. **Report.**

#### `grove-diagnostics`

**Description:** collect a Grove diagnostics bundle for a bug report or offline analysis, or analyse an existing bundle. Read-only; never collects Secrets or ConfigMaps, and strips environment variable values.

**Workflow:**

1. **Scope:** the namespaces and, optionally, the target PodCliqueSet.
2. **Capture:** Grove resources, operator logs and status, Pods, events, the JSON of `tree`, `events` and `topology`, the versions of the CLI, operator and CRDs, and a `manifest.json` recording the Arborist and Grove versions, the producing command and the format of each file. Specs pass through the skill's `jq` filter that removes environment variable values.
3. **Package** a timestamped `.tgz` and report what was and was not collected.
4. **Analyse** (optional): examine a bundle together with the Grove source for the operator's version.

There is no `diagnostics` command: the value is in knowing what to capture, which the skill encodes. The skill is plain Markdown, so a person without an agent can follow the same capture steps.

### RBAC

Arborist needs only read access. See [Commands](#commands) for how a missing permission is reported.

| Command | Reads (`get`/`list`) | Scope |
|---|---|---|
| `tree` | PCS, PCSG, PCLQ, Pods; ResourceClaims and ResourceClaimTemplates for DRA GPU counts | Namespace (or all with `-A`) |
| `events` | PCS, PCSG, PCLQ, Pods, PodGangs (`scheduler.grove.io`); events (`events.k8s.io`) | Namespace (or all with `-A`) |
| `topology` | ClusterTopologyBindings, Nodes, Pods; ResourceSlices, ResourceClaims and ResourceClaimTemplates for DRA; the `--for` target | Cluster |
| `version` | Pods | Operator namespace with `--grove-namespace`, otherwise cluster |
| `skills install` | Nothing | — |

### Distribution

Arborist is distributed through [Krew](https://krew.sigs.k8s.io/) as the `grove` plugin, a name not yet taken in the Krew index. Binaries for Linux and macOS (amd64 and arm64) are built and released from the Grove repository with the same version as the operator and attached to each Grove release, which is also where users of older Grove releases get a matching CLI. The code lives under `tools/arborist/` as its own Go module (see [Open Questions](#open-questions)).

### Backward Compatibility

- **Grove:** no API, operator or chart changes. Arborist reads existing resources only.
- **JSON output:** no stability promise in alpha; a `schemaVersion` field (an additive change) and compatibility rules are introduced at Beta.
- **Skills:** versioned with the binary and reinstalled with `skills install`; they detect a mismatch themselves.
- **Operator versions:** a CLI supports operators of its own major and minor version and reads Grove resources through the clients of that release.

### Monitoring

Arborist is a client-side tool and exposes no metrics or endpoints, and none are planned. Its behaviour is observable through:

- **Exit codes and stderr:** errors and warnings (skew, uncounted devices, the DRA fallback, a `--for` workload on another binding) go to stderr; stdout carries only the requested output.
- **The User-Agent `kubectl-grove/<version>`** on every request, so administrators can attribute Arborist traffic in API server audit logs.
- **`kubectl grove version`** for client and operator skew.

### Dependencies

- **Go modules:** `k8s.io/cli-runtime` (already a dependency of the operator module; it pulls in kustomize's `api` and `kyaml` modules, which grow the binary), `k8s.io/client-go`, Grove's generated clients, `github.com/spf13/cobra` and a JSON Schema generator.
- **Cluster:** Grove installed, `events.k8s.io/v1` (GA since Kubernetes 1.19), and for DRA accounting `resource.k8s.io/v1` (GA since Kubernetes 1.34) with the NVIDIA DRA driver. ClusterTopologyBindings are optional; without one, `topology` groups by node.
- **Node labels:** `nvidia.com/gpu.product` from GPU Feature Discovery for GPU types (`unknown` without it).
- **Krew:** a manifest in the Krew index under the name `grove`.

### Test Plan

**Unit tests** (table-driven, with fake clients):

- `tree`: status per row kind; reason order, including `PodCreateFailed` from `lastErrors` only while Pods are missing; replica rows in JSON and collapse in pretty output; `--only-unhealthy` and `--depth` counted in `omitted`.
- `events`: UID matching; the deleted-Pod fallback (generated suffixes only); the same-name fallback for recreated PodCliques; masking of generated Pod names; grouping, `scope`, ordering, `--since`, `--no-group`; Warning-only default and `omitted.normalEvents`.
- `topology`: row arithmetic, Free floored at 0, terminal Pods excluded, cordoned and NotReady nodes as Unavailable, rows per GPU type, the `(none)` row, `--in` errors, binding selection (none, one, several), the unscheduled footer with gated Pods, `--for`.
- GPU accounting: device plugin with init containers and sidecars, DRA with unallocated and shared claims, both on one node, the fallback when DRA objects cannot be listed, a warning on MIG devices.
- `version`: release tags, digest-only images with the `chart` label, `v0.0.0-g<sha>`, `-dev` and custom tags as `unknown`, minor-version skew, several versions during a rollout, `unavailable`.
- Refs: resolution through a fake RESTMapper, replica refs, kinds each command rejects, a ref with `-A`.

**Output contract:** golden files cover pretty and JSON output; every JSON output in the tests is validated against its generated schema; `--schema` works without a cluster.

**End-to-end tests** on Grove's existing k3d-based e2e setup: each command against healthy and broken workloads, a user with namespace-only RBAC (`topology` fails naming the permission), and a cluster without Grove.

**Skill acceptance tests:** a curated subset of the POC's broken workloads (see [Appendix](#poc-error-scenarios)) is applied, the skill is run several times per scenario, and a run passes when its report names the scenario's documented root cause. Pass rates are recorded at each milestone. The POC scenarios involve no GPUs, so `topology` scenarios (such as the fragmentation case above) are added, and a diagnostics test checks that a canary environment value and a Secret never reach the bundle.

**Value validation:** before the first release, on the same scenarios and on a KWOK-scale cluster, an agent using the Arborist skills is compared with an agent using a kubectl-only baseline skill, with several runs per scenario, on root-cause accuracy, tool calls, tokens and wall-clock time. `tree`, `events` and `topology` are each kept only if accuracy is at least as good as the baseline and at least one of the other measures improves.

**Scale tests** on Grove's [KWOK](https://kwok.sigs.k8s.io/)-based scale cluster (`make -C operator scale-cluster-up`) at hundreds of PodCliqueSets, thousands of Pods and hundreds of nodes: requests, latency and memory per command, recorded for each milestone.

### Graduation Criteria

#### Alpha

- `tree`, `events`, `topology` and `version` with pretty output, `-o json` and `--schema`, and device-plugin and DRA GPU accounting.
- `grove-debug`, `grove-topology` and `grove-diagnostics` with the shared reference, and `skills install`.
- Delivered in two PRs: first the CLI, then the skills with `skills install`, which embeds them and has nothing to install without them. The CLI lands first because the skills are written against its flags and JSON.
- Value validation done, and any command without a gain removed.
- Published through Krew.

#### Beta

- JSON output carries `schemaVersion`, with documented compatibility rules (additive changes within a version).
- KWOK scale results documented, with pagination added where they show a need.
- Acceptance-test pass rates recorded for two releases.
- User documentation in the Grove docs, including RBAC requirements and the version policy.

#### GA

- Two releases at Beta without breaking changes to flags or JSON.
- A deprecation policy for flags and JSON fields published in the documentation.

## Implementation History

- **2026-01:** Tracking issue [#373](https://github.com/ai-dynamo/grove/issues/373) opened; first proposal for a `kubectl-grove` CLI ([#374](https://github.com/ai-dynamo/grove/pull/374)).
- **2026-03:** POC on the `arborist` branch: a TUI with forest and topology views, a topology command, a diagnostics collector, and device-plugin and DRA GPU accounting.
- **2026-04:** GREP proposed in [#508](https://github.com/ai-dynamo/grove/pull/508), centred on the TUI.
- **2026-10:** Design revised to a minimal read-only CLI plus agent skills.

## Alternatives

### Interactive terminal UI

A [Bubble Tea](https://github.com/charmbracelet/bubbletea) TUI with a forest view of the hierarchy and a topology view, drill-down navigation, a contextual events pane, YAML and log overlays, and live updates from informers. A POC was built.

*Rejected because:* interactive drill-down mainly helps a person walk the hierarchy and correlate events, which an agent can do itself given good primitives. A TUI cannot be scripted, tested easily, used from CI or driven by an agent, and it concentrated the largest risks: informer memory on large clusters, snapshot rebuild latency, rendering performance and cursor stability. Its YAML and log views duplicated kubectl. Every user story it served is covered by the CLI and skills. Dropping it also drops the plan for a k9s plugin to launch its views; a documented k9s shortcut running `tree` or `events` on the selected resource could be added later.

### Improving Grove's status so kubectl is enough

Richer conditions and events on Grove's resources would help everyone and are worth doing independently. *Rejected as a replacement because:* conditions summarise availability by design (`MinAvailableBreached` counts starting Pods), and the other questions need joins that no single object's status can carry: events across a subtree, GPU capacity per topology domain, and Grove against non-Grove usage.

### Other tool shapes

- **An MCP server instead of a CLI and skills.** It would expose the same views as agent tools. *Rejected because:* it serves only agents that support MCP, not people or scripts (Story 3), adds a process to install and run, and still needs the Grove knowledge the skills carry. It could wrap the CLI later.
- **Generic owner-tree plugins** such as `kubectl tree`. *Rejected because:* they know nothing of virtual replica levels, health roll-up from Pod reasons, GPU accounting or event matching, and PodGangs are not in the owner chain of the Pods they group.
- **A `status` command**, a one-call summary of blockers, event reasons, abnormal conditions, rollout and gang state. *Rejected because:* it is a composition of `tree --only-unhealthy`, `events` and kubectl, which the skills describe; it adds no primitive.
- **A `diagnostics` command or shipped script.** The POC had a Go collector, and a script (`grove-diagnostics.sh`) would work without an agent. *Rejected because:* the value is in knowing what to capture, which the skill encodes and can also analyse; capturing is kubectl and Arborist.
- **One `grove` skill**, or a separate `grove-capacity` skill. *Rejected because:* one skill needs a broad trigger and loads a large context for every question; `grove-capacity` can be split from `grove-topology` later if it grows too large.
- **Two GREPs, one for the CLI and one for the skills.** *Rejected because:* the CLI's value is validated through a skill, so a CLI GREP could not graduate before a skills GREP, and `skills install` would install skills not yet approved. Two PRs give the review split without splitting the design.

### Output formats

- **[TOON](https://github.com/toon-format/toon) (`-o toon`).** A token-efficient, lossless encoding of the JSON data model; its own benchmarks report 35–59% fewer tokens than JSON on flat, uniform data, and an official Go library exists ([`toon-go`](https://github.com/toon-format/toon-go)). *Rejected for now because:* the savings are self-reported and unvalidated on Arborist's output and the models operators use, models are far more practised on JSON, minimal JSON and filters already capture most of the savings, the Go library only reached v1.0 in October 2026, and TOON has no schema language. Adding it later is non-breaking, since `toon-go` reads the same `json` tags.
- **An `apiVersion`/`kind` envelope.** Self-describing at rest and familiar to Kubernetes users. *Rejected because:* CLIs such as `gh`, `docker` and `helm` emit bare payloads, an API-style envelope implies stability that alpha cannot promise, and `kind` would repeat the command. Bundles are described by `manifest.json`, and `schemaVersion` comes at Beta.
- **A compact text format for agents (`-o agent`).** *Rejected because:* a third format to maintain and test, which scripts would start to parse.
- **`-o wide`.** *Rejected because:* JSON already carries every field, the useful extra columns fit in the default output, and listing every object of every event group would bury the grouping; `events --no-group` gives per-object rows.
- **JSON that ignores the default bounds.** *Rejected because:* pretty and JSON output would show different records for the same command, and agents would pay tokens for noise such as every Normal event of a rollout.
- **Switching to JSON when stdout is not a terminal.** *Rejected because:* the same command would print differently in a terminal and in a pipe.

### Cluster access and output

- **cli-runtime's printers (`PrintFlags`, `JSONPrinter`).** They would give kubectl's output formats for free. *Rejected because:* they are built for Kubernetes API objects, not for Arborist's output. `JSONPrinter` accepts only a `runtime.Object`, fails with "missing apiVersion or kind" when the object has neither (the envelope rejected above), and otherwise does little more than `json.MarshalIndent`. `PrintFlags` would also bring `yaml`, `kyaml`, `name` and seven `jsonpath` and template variants into the contract. `jq` over `-o json` covers the same needs.
- **Hand-rolled kubeconfig handling** (the POC's approach). *Rejected because:* every kubectl connection flag, the loading order, namespace defaulting and the discovery cache would have to be reimplemented and kept in step with kubectl; the POC lacked `--context` and `--kubeconfig` and preferred in-cluster configuration, so inside a Pod it ignored the user's kubeconfig.
- **kubectl's command factory (`k8s.io/kubectl`).** *Rejected because:* a heavy dependency whose client methods are thin wrappers over the configuration Arborist already has.
- **Keeping the POC's [kong](https://github.com/alecthomas/kong) framework.** *Rejected because:* kubectl's flags would have to be mapped into it by hand; cli-runtime binds directly to the flag set cobra provides.

### Events scope

- **HPA, ResourceClaim and Node events.** HPAs that Grove creates for scaling explain autoscaling problems; DRA allocation failures surface on ResourceClaims; node events explain evictions. *Rejected for now because:* subtrees are kept to Grove resources and Pods. The Pod's scheduling failure event usually references a failing claim, and all three are candidates for [future work](#future-work).
- **Including ancestor events,** all of them or those whose messages name a child. *Rejected because:* including all defeats scoping (run `events` on the parent instead), and matching names in messages depends on wording.
- **Matching by name prefix.** *Rejected because:* ambiguous, and unable to tell a recreated object from its predecessor.
- **No same-name fallback for recreated resources,** leaving their history to `kubectl events --for`. *Rejected because:* the events that explain a gang termination would be missing from the very view meant to explain it.
- **Normalising numbers in messages.** *Rejected because:* `0/16 nodes` and `0/15 nodes` can be different situations; only generated Pod names are masked.

### `tree` roll-up

- **Rolling up Pod reasons only,** and having `grove-debug` run `events` on every unhealthy group when Pods are missing. *Rejected because:* `tree` would then point at the gated siblings of a clique whose Pods cannot be created.

### Topology command

- **A positional binding name (`topology <binding> [level]`).** Explicit and identical on every cluster. *Rejected because:* `-t` with a single-binding default, like `-n`, keeps commands short on clusters with one topology, and omitting it with several bindings is an error anyway.
- **Leaving unlabelled nodes out** and counting them in `omitted`. *Rejected because:* rows would no longer add up to the totals, and a `(none)` row shows the misconfiguration directly.
- **Reusing code from [Topograph](https://github.com/NVIDIA/topograph).** Topograph discovers network topology and writes it as node labels. *Rejected because:* it does the opposite job, producing labels where Arborist reads them, and nothing in it groups nodes by a binding's keys or counts GPUs (its DRA provider reads an NVLink clique label, not ResourceSlices). Its potentially reusable packages are internal to its module, and importing it would move Arborist to newer Go and Kubernetes library versions than Grove uses. Its label conventions may still inform `grove-topology`, and its KWOK node models may help scale tests.

### Version checking

- **A skew warning on every command.** *Rejected because:* it adds an API read to every command and fails for users who cannot read the operator's namespace; none of the surveyed CLIs does it.
- **Operator-published build information** (an annotation on the operator's Pod, a ConfigMap, or a chart annotation). *Rejected because:* the annotation needs operator and chart changes that would help only contributors' local builds; a ConfigMap can outlive the operator that wrote it, for example after a rollback, and then report a version that is not running; a chart annotation repeats the image tag and `chart` label.
- **Exact version matching.** *Rejected because:* Krew's index carries only the latest version of a plugin, so users on any other Grove release would see a warning even when their CLI works.
- **Downloading source in the CLI.** *Rejected because:* Arborist makes no requests outside the cluster; the agent downloads source, with the user's agreement.

### Skill distribution

- **Manual copying only**, or **a third-party skill installer only** (such as `npx skills`), or **an agent-specific plugin marketplace.** *Rejected as the primary path because:* none gives operators a version-matched install on every agent. All remain possible alongside `skills install`, since the skills are ordinary `SKILL.md` folders.

## Open Questions

- **PodGang events.** PodGangs are included for now, but they are scheduler API objects that users don't author, owned by the PodCliqueSet rather than by the objects whose Pods they group. Should they be included by default? The same applies to [PodGangMaps](../393-coherent-rolling-updates/README.md), on which Grove records no events today and which are not included.
- **Scheduler-specific objects.** [Scheduler backends](../375-scheduler-backend-framework/README.md) create their own objects for a PodGang (KAI and Volcano PodGroups, Kueue Workloads) and may record gang-level failures there rather than on the Pods. Should `events` follow a PodGang to them?
- **Grove-created ResourceClaims.** With [resource sharing](../390-hierarchical-resource-sharing/README.md), Grove creates ResourceClaims owned by its PodCliqueSets, scaling groups and PodCliques, where DRA allocation failures are recorded. Should their events count as part of the subtree, although they are not Grove resources?
- **Topology narrowing.** Is `topology <level> --in level=value,...` the right shape? An all-positional form (`topology zone=a block=b2 rack`) and a nested multi-level tree with `--depth` were also considered.
- **Module location.** This proposal places Arborist in `tools/arborist/`. The repository already has an empty `cli-plugin/` Go module, created at the start of the project as a placeholder for a kubectl plugin. Should Arborist live there instead, or should the placeholder be removed?

## Future Work

- `--watch` on `tree` and `topology`, and/or a Grove-aware `wait`: which of the two, how `--watch` output looks in JSON, and whether it needs more than list and watch.
- `topology --pods` (see [above](#topology)).
- More event sources (HPAs, ResourceClaims, nodes) and a `--reason` filter.
- A `schema` subcommand listing all schemas, and shell completion of resource names.
- Counting MIG, partitioned and shared GPUs.
- Reconsidering TOON once its benefits are measured on Arborist's output.

## Appendix

### POC error scenarios

The POC branch has 22 deliberately broken workloads, most in a plain and a scaling-group variant, covering image errors, container failures, scheduling constraints, resource requests and quotas, and mixed or cascading failures. Expected-state documents exist for the image and container groups. Three scenarios do not fail as written and must be fixed before use, and none involves GPUs or topology. The full list, with each scenario's root cause, evidence and first diagnostic step, will be tracked in the test issue.

### Version detection prior art

How other Kubernetes CLIs find the server-side version (checked in their source):

| Tool | Server version from | Where skew is reported |
|---|---|---|
| kubectl | `/version` endpoint, readable by all users | `kubectl version` only |
| kueuectl, flux | Controller Deployment image tag, found by label in a default namespace | Not on every command |
| linkerd | `linkerd-config` ConfigMap | `linkerd check` (a warning) |
| velero | Creates a `ServerStatusRequest` that the server fills in (a write, which Arborist rules out) | `velero version` only |
| cmctl | CRD version label, then the CRD conversion webhook's Service, then the Pod's image tag | Not on every command |
| KubeVirt | Version API readable by `system:authenticated` | — |
