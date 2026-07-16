# Grove + Kueue kind demo (2-node, hostname TAS)

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

`setup-kueue-kind-demo.sh` labels every node with `topology.ai-dynamo.io/tas-node=true`,
applies `grove-kind-topology.yaml` (host -> `kubernetes.io/hostname`), and applies
`kueue-kind-queue.yaml`. Grove syncs the `ClusterTopologyBinding` to a Kueue
`Topology` with the same name.

## Demos

### Partial admission via minCount (primary)

`grove-kueue-simple-mincount.yaml` requests `replicas: 4`, `minAvailable: 2`, and
`pack.required: host`, with each Pod requesting 1 CPU. The demo ClusterQueue has
only 2 CPU of quota, so Kueue admits the Workload at `minCount=2`: 2 of the 4 Pods
run (one per node via hostname TAS) and the other 2 stay scheduling-gated.

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
