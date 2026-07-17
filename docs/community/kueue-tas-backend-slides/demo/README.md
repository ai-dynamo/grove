# Grove + Kueue kind demo (2-node, rack TAS + hostname anti-affinity)

## Cluster

Create a 2-node kind cluster (control-plane + 1 worker):

```sh
cd operator
make kind-down
make kind-up WORKERS=1
```

Install Kueue and deploy Grove as usual, then prepare the demo platform:

```sh
docs/community/kueue-tas-backend-slides/demo/setup-kueue-kind-demo.sh
kubectl get topology grove-kind-topology
```

`setup-kueue-kind-demo.sh` enables Kueue's `PartialAdmission` feature gate, labels
both nodes as members of `rack-0`, applies `grove-kind-topology.yaml` (rack only),
and applies `kueue-kind-queue.yaml`. Grove syncs the `ClusterTopologyBinding` to a
Kueue `Topology` with the same name. Hostname is intentionally not a TAS level:
Kueue would otherwise pin pods to one host and fight hostname anti-affinity.

## Demos

### Partial admission via minCount (primary)

`grove-kueue-simple-mincount.yaml` requests `replicas: 4`, `minAvailable: 2`, and
`pack.required: rack`, with each Pod requesting 1 CPU. The demo ClusterQueue has
only 2 CPU of quota, so Kueue admits the Workload at `minCount=2` into `rack-0`.
Required hostname anti-affinity places those 2 Pods on different nodes; the other
2 Pods must remain scheduling-gated.

```sh
docs/community/kueue-tas-backend-slides/demo/validate-grove-kueue-mincount.sh
```

### PCSG all-or-nothing

`grove-kueue-pcsg.yaml` uses the same hostname topology. The base PodGang requests
six Pods across two PCSG replicas; it exercises prebuilt Workload shape more than
2-node placement.

```sh
docs/community/kueue-tas-backend-slides/demo/validate-grove-kueue-pcsg.sh
```

## Cleanup

```sh
kubectl delete -f docs/community/kueue-tas-backend-slides/demo/grove-kueue-simple-mincount.yaml --ignore-not-found
kubectl delete -f docs/community/kueue-tas-backend-slides/demo/grove-kueue-pcsg.yaml --ignore-not-found
```
