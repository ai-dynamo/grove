#!/bin/bash
set -e

NAMESPACE="${GRAFANA_NAMESPACE:-monitoring}"

echo "Creating ConfigMap with Grove dashboards..."

kubectl create configmap grove-dashboards \
  --from-file=grove-controller-dashboard.json=operator/grove-controller-dashboard.json \
  --from-file=grove-pyroscope-dashboard.json=operator/grove-pyroscope-dashboard.json \
  --namespace="${NAMESPACE}" \
  --dry-run=client -o yaml | kubectl apply -f -

echo "✓ ConfigMap created in namespace: ${NAMESPACE}"
echo ""
echo "Now upgrade Grafana with the updated values:"
echo "  helm upgrade grafana grafana/grafana -n ${NAMESPACE} -f operator/grafana-values.yaml"
echo ""
echo "Or if you want to manually import:"
echo "1. Port-forward to Grafana: kubectl port-forward -n ${NAMESPACE} svc/grafana 3000:80"
echo "2. Open http://localhost:3000 (admin/admin)"
echo "3. Go to Dashboards → Import"
echo "4. Upload operator/grove-controller-dashboard.json"
echo "5. Upload operator/grove-pyroscope-dashboard.json"