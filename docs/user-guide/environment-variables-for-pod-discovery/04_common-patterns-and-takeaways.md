# Common Patterns and Takeaways

This guide covers practical patterns for using Grove environment variables in your applications, along with key takeaways.

## Common Patterns for Using Environment Variables

Here are some common patterns for using Grove environment variables in your applications:

### Pattern 1: Constructing Pod FQDNs

To construct the FQDN for any pod in your PodClique:

```bash
# For your own FQDN
MY_FQDN="$GROVE_PCLQ_NAME-$GROVE_PCLQ_POD_INDEX.$GROVE_HEADLESS_SERVICE"

# For another pod in the same PodClique (e.g., pod index 3)
OTHER_POD_INDEX=3
OTHER_POD_FQDN="$GROVE_PCLQ_NAME-$OTHER_POD_INDEX.$GROVE_HEADLESS_SERVICE"
```

### Pattern 2: Finding the Leader in a PCSG

If you're in a worker pod and need to connect to the leader (assuming the leader PodClique is named "leader"):

```bash
# Construct the leader's PodClique name: PCSG name + PCSG index + "-leader"
LEADER_PCLQ_NAME="$GROVE_PCSG_NAME-$GROVE_PCSG_INDEX-leader"

# Leader is typically at index 0
LEADER_FQDN="$LEADER_PCLQ_NAME-0.$GROVE_HEADLESS_SERVICE"
```

### Pattern 3: Discovering All Peers in a PodClique

If you need to construct addresses for all pods in your PodClique:

```bash
# Assuming you have a way to know the total number of replicas in your PodClique
# (this could be passed in as a custom env var or ConfigMap)
TOTAL_REPLICAS=5

for i in $(seq 0 $((TOTAL_REPLICAS - 1))); do
  PEER_FQDN="$GROVE_PCLQ_NAME-$i.$GROVE_HEADLESS_SERVICE"
  echo "Peer $i: $PEER_FQDN"
done
```

### Pattern 4: Determining Your Role in a PCSG

You can use the `GROVE_PCLQ_NAME` to determine which role this pod plays:

```bash
# Extract the role from the PodClique name
# The role is typically the last component after the final hyphen
ROLE=$(echo $GROVE_PCLQ_NAME | awk -F- '{print $NF}')

if [ "$ROLE" = "leader" ]; then
  echo "I am a leader pod"
elif [ "$ROLE" = "worker" ]; then
  echo "I am a worker pod"
fi
```

### Pattern 5: Using Headless Service for Pod Discovery

The `GROVE_HEADLESS_SERVICE` provides a DNS name that resolves to all pods in the PodCliqueSet replica:

```bash
# This will return DNS records for all pods in the same PodCliqueSet replica
nslookup $GROVE_HEADLESS_SERVICE
```

---

## Key Takeaways

1. **Automatic Context Injection**  
   Grove injects a consistent set of environment variables into every pod, giving each container precise runtime context about *where it sits* in the PodCliqueSet hierarchy.

2. **Explicit, Predictable Addressing**  
   Grove does not hide pod topology. Instead, it provides the building blocks (`GROVE_PCS_NAME`, `GROVE_PCLQ_NAME`, `GROVE_PCSG_NAME`, indices, and the headless service domain) so applications can **explicitly construct the addresses they need**, including those of other PodCliques.

3. **Stable Pod Identity**  
   `GROVE_PCLQ_POD_INDEX` gives each pod a stable, deterministic identity within its PodClique, making it easy to assign ranks, shard work, or implement leader/worker logic.

4. **Scaling-Group Awareness**  
   For pods in a PodCliqueScalingGroup, Grove exposes additional variables that identify the PCSG replica and its composition. This allows components to understand which *logical unit (super-pod)* they belong to and how many peers are expected.

5. **Designed for Distributed Systems**  
   Grove's environment variables are intentionally low-level and composable. They are meant to support a wide range of distributed system patterns—leader election, sharding, rendezvous, collective communication—without imposing a fixed discovery or coordination model.
