# Pyroscope Integration for Grove Operator

## Deployment Status

Pyroscope has been deployed in the `pyroscope` namespace:
- Service URL: `http://pyroscope.pyroscope.svc.cluster.local:4040`

## Configure Grove Operator

Add the following to your Helm values file to enable profiling:

```yaml
deployment:
  labels:
    app.kubernetes.io/component: operator-deployment
    app.kubernetes.io/name: grove-operator
    app.kubernetes.io/part-of: grove
  env:
    - name: GROVE_INIT_CONTAINER_IMAGE
      value: grove-initc
    - name: PYROSCOPE_SERVER_URL
      value: http://pyroscope.pyroscope.svc.cluster.local:4040
```

## Deploy/Upgrade Grove Operator

```bash
helm upgrade --install grove-operator ./operator/charts \
  --set deployment.env[1].name=PYROSCOPE_SERVER_URL \
  --set deployment.env[1].value=http://pyroscope.pyroscope.svc.cluster.local:4040
```

## Access Pyroscope UI

Port-forward to access the Pyroscope UI:

```bash
kubectl port-forward -n pyroscope svc/pyroscope 4040:4040
```

Then open: http://localhost:4040

## Integration with Existing Prometheus

Since Prometheus is in the monitoring namespace, you can:

1. Add Prometheus metrics scraping for Pyroscope:
```yaml
apiVersion: v1
kind: Service
metadata:
  name: pyroscope-metrics
  namespace: pyroscope
  annotations:
    prometheus.io/scrape: "true"
    prometheus.io/port: "4040"
spec:
  ports:
  - name: metrics
    port: 4040
  selector:
    app.kubernetes.io/name: pyroscope
```

2. Create a ServiceMonitor (if using Prometheus Operator):
```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: pyroscope
  namespace: monitoring
spec:
  namespaceSelector:
    matchNames:
    - pyroscope
  selector:
    matchLabels:
      app.kubernetes.io/name: pyroscope
  endpoints:
  - port: metrics
    interval: 30s
```

## Profiles Collected

The operator collects:
- CPU profiles
- Memory allocations (objects and space)
- In-use memory (objects and space)

## Next Steps

1. Deploy the updated operator with Pyroscope enabled
2. Generate some load on the operator
3. View profiles in the Pyroscope UI
4. Optionally add Grafana dashboard for combined metrics + profiles view
